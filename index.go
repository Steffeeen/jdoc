package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

var errUnavailable = errors.New("version unavailable")

type member struct {
	Signature stringID
	Anchor    stringID
}

type javaType struct {
	FQN     stringID
	Path    stringID
	Members map[stringID][]member
}

type versionIndex struct {
	Version int
	Root    string
	Strings *stringTable
	Types   map[stringID][]*javaType
}

// Published indices are immutable; only the catalog changes during discovery.
type catalog struct {
	mu       sync.RWMutex
	versions map[int]*versionIndex
	latest   int
}

func (c *catalog) publish(idx *versionIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.versions == nil {
		c.versions = make(map[int]*versionIndex)
	}
	c.versions[idx.Version] = idx
	if idx.Version > c.latest {
		c.latest = idx.Version
	}
}

func (c *catalog) get(version int) (*versionIndex, int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if version == 0 {
		version = c.latest
	}
	return c.versions[version], c.latest
}

type loader struct {
	client  *http.Client
	origin  string
	retries int
	backoff time.Duration
	strings stringTable
	classes map[stringID][]*javaType
}

// Only sequential indexing touches this cache. Shared records are never mutated.
func (l *loader) shareClass(t *javaType) *javaType {
variants:
	for _, existing := range l.classes[t.FQN] {
		if existing.Path != t.Path || len(existing.Members) != len(t.Members) {
			continue
		}
		for name, members := range t.Members {
			other, ok := existing.Members[name]
			if !ok || !slices.Equal(members, other) {
				continue variants
			}
		}
		return existing
	}
	if l.classes == nil {
		l.classes = make(map[stringID][]*javaType)
	}
	l.classes[t.FQN] = append(l.classes[t.FQN], t)
	return t
}

func newLoader() *loader {
	return &loader{
		client: &http.Client{
			Timeout:       45 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		origin: "https://docs.oracle.com", retries: 2, backoff: time.Second,
	}
}

func (l *loader) root(version int) string {
	if version <= 10 {
		return fmt.Sprintf("%s/javase/%d/docs/api/", l.origin, version)
	}
	return fmt.Sprintf("%s/en/java/javase/%d/docs/api/", l.origin, version)
}

var landingPath = regexp.MustCompile(`^/(?:en/java/)?javase/([0-9]+)/(?:index\.html)?$`)

func missingRedirect(resp *http.Response, version int) bool {
	if resp.StatusCode != http.StatusMovedPermanently && resp.StatusCode != http.StatusFound && resp.StatusCode != http.StatusTemporaryRedirect && resp.StatusCode != http.StatusPermanentRedirect {
		return false
	}
	u, err := resp.Location()
	if err != nil || u.Scheme != "https" || u.Host != "docs.oracle.com" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	m := landingPath.FindStringSubmatch(u.Path)
	if len(m) == 0 {
		return false
	}
	n, _ := strconv.Atoi(m[1])
	return n != version
}

func (l *loader) fetch(ctx context.Context, version int, file string, probe bool) ([]byte, error) {
	address := l.root(version) + file
	var last error
	for attempt := 0; attempt <= l.retries; attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(l.backoff * time.Duration(1<<(attempt-1)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "jdoc/1.0")
		resp, err := l.client.Do(req)
		if err != nil {
			last = err
			continue
		}
		if probe && (resp.StatusCode == http.StatusNotFound || missingRedirect(resp, version)) {
			resp.Body.Close()
			return nil, errUnavailable
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			last = fmt.Errorf("GET %s: HTTP %d", address, resp.StatusCode)
			if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
				continue
			}
			return nil, last
		}
		const limit = 32 << 20
		data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		resp.Body.Close()
		if err != nil {
			last = err
			continue
		}
		if len(data) > limit {
			return nil, fmt.Errorf("GET %s: index exceeds %d bytes", address, limit)
		}
		return data, nil
	}
	return nil, fmt.Errorf("GET %s: %w", address, last)
}

type indexEntry struct {
	Package string `json:"p"`
	Label   string `json:"l"`
	Class   string `json:"c"`
	Module  string `json:"m"`
	URL     string `json:"u"`
	OldURL  string `json:"url"`
}

func decodeIndex(data []byte, name string) ([]indexEntry, error) {
	data = bytes.TrimSpace(data)
	assignment := []byte(name + "SearchIndex")
	if !bytes.HasPrefix(data, assignment) {
		return nil, fmt.Errorf("invalid %s search index assignment", name)
	}
	data = bytes.TrimSpace(data[len(assignment):])
	if len(data) == 0 || data[0] != '=' {
		return nil, fmt.Errorf("invalid %s search index assignment", name)
	}
	data = bytes.TrimSpace(data[1:])
	if len(data) == 0 || data[0] != '[' {
		return nil, fmt.Errorf("invalid %s search index array", name)
	}
	var entries []indexEntry
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode %s index: %w", name, err)
	}
	tail := strings.TrimSpace(string(data[decoder.InputOffset():]))
	tail = strings.TrimSpace(strings.TrimPrefix(tail, ";"))
	if tail != "" && tail != "updateSearchResults();" {
		return nil, fmt.Errorf("unexpected %s index suffix", name)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("empty %s index", name)
	}
	return entries, nil
}

func (e indexEntry) override() string {
	if e.URL != "" {
		return e.URL
	}
	return e.OldURL
}

func safePage(root, relative string) (string, error) {
	base, _ := url.Parse(root)
	u, err := url.Parse(relative)
	if err != nil {
		return "", err
	}
	u = base.ResolveReference(u)
	if u.Scheme != base.Scheme || u.Host != base.Host || !strings.HasPrefix(path.Clean(u.Path), base.Path) || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("invalid type URL %q", relative)
	}
	return u.String(), nil
}

func (l *loader) buildIndex(version int, root string, types, members, packages []indexEntry) (*versionIndex, error) {
	modules := make(map[string]string)
	for _, p := range packages {
		modules[p.Label] = p.Module
	}
	idx := &versionIndex{Version: version, Root: root, Strings: &l.strings, Types: make(map[stringID][]*javaType)}
	byFQN := make(map[string]*javaType)
	for _, e := range types {
		if e.Package == "" || e.Label == "" {
			continue // Synthetic navigation entries are not Java types.
		}
		page := e.override()
		if page == "" {
			page = strings.ReplaceAll(e.Package, ".", "/") + "/" + e.Label + ".html"
			if version >= 11 {
				module := e.Module
				if module == "" {
					module = modules[e.Package]
				}
				if module == "" {
					return nil, fmt.Errorf("missing module for %s.%s", e.Package, e.Label)
				}
				page = module + "/" + page
			}
		}
		address, err := safePage(root, page)
		if err != nil {
			return nil, err
		}
		fqn := e.Package + "." + e.Label
		if byFQN[fqn] != nil {
			continue
		}
		t := &javaType{FQN: l.strings.intern(fqn), Path: l.strings.intern(strings.TrimPrefix(address, root)), Members: make(map[stringID][]member)}
		byFQN[fqn] = t
		aliases := map[string]bool{strings.ToLower(fqn): true, strings.ToLower(e.Label): true, strings.ToLower(e.Label[strings.LastIndex(e.Label, ".")+1:]): true}
		for alias := range aliases {
			id := l.strings.intern(alias)
			idx.Types[id] = append(idx.Types[id], t)
		}
	}
	count := 0
	for _, e := range members {
		t := byFQN[e.Package+"."+e.Class]
		if t == nil || e.Label == "" {
			continue
		}
		anchor := e.override()
		if anchor == "" {
			anchor = e.Label
			if version == 9 {
				anchor = strings.NewReplacer("(", "-", ")", "-", ",", "-", " ", "", "[]", ":A").Replace(anchor)
			} else {
				anchor = strings.ReplaceAll(anchor, " ", "")
				if simple := e.Class[strings.LastIndex(e.Class, ".")+1:]; strings.HasPrefix(anchor, simple+"(") {
					anchor = "<init>" + anchor[len(simple):]
				}
			}
		}
		// Normalize existing percent escapes without double-encoding constructor anchors.
		decoded, err := url.PathUnescape(anchor)
		if err != nil {
			return nil, fmt.Errorf("invalid member anchor %q: %w", anchor, err)
		}
		u := &url.URL{Fragment: decoded}
		anchor = strings.TrimPrefix(u.String(), "#")
		name, _, _ := strings.Cut(e.Label, "(")
		key := l.strings.intern(strings.ToLower(name))
		t.Members[key] = append(t.Members[key], member{Signature: l.strings.intern(e.Label), Anchor: l.strings.intern(anchor)})
		count++
	}
	if len(byFQN) == 0 || count == 0 {
		return nil, errors.New("index contains no searchable types or members")
	}
	shared := make(map[*javaType]*javaType, len(byFQN))
	for _, t := range byFQN {
		shared[t] = l.shareClass(t)
	}
	for _, types := range idx.Types {
		for i, t := range types {
			types[i] = shared[t]
		}
	}
	return idx, nil
}

func (l *loader) load(ctx context.Context, version int) (*versionIndex, error) {
	data, err := l.fetch(ctx, version, "type-search-index.js", true)
	if err != nil {
		return nil, err
	}
	types, err := decodeIndex(data, "type")
	if err != nil {
		return nil, err
	}
	var members, packages []indexEntry
	var memberErr, packageErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		b, err := l.fetch(ctx, version, "member-search-index.js", false)
		if err == nil {
			members, err = decodeIndex(b, "member")
		}
		memberErr = err
	}()
	go func() {
		defer wg.Done()
		b, err := l.fetch(ctx, version, "package-search-index.js", false)
		if err == nil {
			packages, err = decodeIndex(b, "package")
		}
		packageErr = err
	}()
	wg.Wait()
	if err := errors.Join(memberErr, packageErr); err != nil {
		return nil, err
	}
	return l.buildIndex(version, l.root(version), types, members, packages)
}

func (l *loader) discover(ctx context.Context, c *catalog) error {
	_, latest := c.get(0)
	for version := max(9, latest+1); ; version++ {
		slog.Info("indexing Javadocs", "version", version)
		idx, err := l.load(ctx, version)
		if errors.Is(err, errUnavailable) && version > 9 {
			slog.Info("no newer Javadocs available", "latest", version-1)
			return nil
		}
		if err != nil {
			return fmt.Errorf("index JDK %d: %w", version, err)
		}
		c.publish(idx)
		slog.Info("indexed Javadocs", "version", version)
	}
}
