package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIndexDecodeWrappers(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		wantErr     bool
	}{
		{"old", `typeSearchIndex = [{"p":"java.lang","l":"String","url":"old.html"}];`, false},
		{"new", " \n typeSearchIndex = [{\"p\":\"java.lang\",\"l\":\"String\",\"u\":\"new.html\"}];updateSearchResults(); \n", false},
		{"bare assignment", `typeSearchIndex=[{"l":"String"}]`, false},
		{"whitespace suffix", `typeSearchIndex=[{"l":"String"}];  updateSearchResults();`, false},
		{"wrong name", `memberSearchIndex=[{"l":"String"}];`, true},
		{"missing assignment", `[{"l":"String"}]`, true},
		{"missing equals", `typeSearchIndex [{"l":"String"}]`, true},
		{"name prefix", `typeSearchIndexExtra=[{"l":"String"}]`, true},
		{"empty input", ``, true},
		{"missing value", `typeSearchIndex=`, true},
		{"object", `typeSearchIndex={"l":"String"};`, true},
		{"null", `typeSearchIndex=null;`, true},
		{"empty array", `typeSearchIndex=[];`, true},
		{"truncated", `typeSearchIndex=[{"l":"String"}`, true},
		{"wrong field type", `typeSearchIndex=[{"l":42}];`, true},
		{"html", `<html>not an index</html>`, true},
		{"html suffix", `typeSearchIndex=[{"l":"String"}];<html>error</html>`, true},
		{"second array", `typeSearchIndex=[{"l":"String"}];[]`, true},
		{"extra semicolon", `typeSearchIndex=[{"l":"String"}];;`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := decodeIndex([]byte(tc.input), "type")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decodeIndex accepted %q: %+v", tc.input, entries)
				}
				return
			}
			if err != nil || len(entries) != 1 || entries[0].Label != "String" {
				t.Fatalf("decodeIndex = %+v, %v", entries, err)
			}
			if tc.name == "old" && entries[0].override() != "old.html" || tc.name == "new" && entries[0].override() != "new.html" {
				t.Fatalf("lost URL override: %+v", entries[0])
			}
		})
	}
	for _, name := range []string{"member", "package"} {
		if _, err := decodeIndex([]byte(name+`SearchIndex=[{"l":"entry"}];updateSearchResults();`), name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestIndexBuildAnchorsAndAliases(t *testing.T) {
	for _, version := range []int{9, 10, 11, 25} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			root := fmt.Sprintf("https://docs.oracle.com/api/%d/", version)
			types := []indexEntry{{Package: "java.util", Label: "Map.Entry"}, {Label: "All Classes", URL: "allclasses-index.html"}, {Package: "java.util"}, {Package: "java.util", Label: "Map.Entry"}}
			members := []indexEntry{
				{Package: "java.util", Class: "Map.Entry", Label: "Entry(java.lang.String[])"},
				{Package: "java.util", Class: "Map.Entry", Label: "put(java.lang.String, int[])"},
				{Package: "java.util", Class: "Map.Entry", Label: "put(int)"},
				{Package: "java.util", Class: "Map.Entry", Label: "VALUE"},
				{Package: "java.util", Class: "Absent", Label: "ignored()"},
				{Package: "java.util", Class: "Map.Entry"},
			}
			idx, err := buildIndex(version, root, types, members, []indexEntry{{Label: "java.util", Module: "java.base"}})
			if err != nil {
				t.Fatal(err)
			}
			if idx.Version != version || len(idx.Types) != 3 {
				t.Fatalf("unexpected index: %+v", idx)
			}
			var typ *javaType
			for _, alias := range []string{"java.util.map.entry", "map.entry", "entry"} {
				got := idx.Types[alias]
				if len(got) != 1 {
					t.Fatalf("alias %q: %+v", alias, got)
				}
				if typ != nil && typ != got[0] {
					t.Fatal("aliases do not share the same type")
				}
				typ = got[0]
			}
			page := "java/util/Map.Entry.html"
			if version >= 11 {
				page = "java.base/" + page
			}
			if typ.Name != "Map.Entry" || typ.FQN != "java.util.Map.Entry" || typ.URL != root+page {
				t.Fatalf("type = %+v", typ)
			}
			constructor, method := "%3Cinit%3E(java.lang.String%5B%5D)", "put(java.lang.String,int%5B%5D)"
			if version == 9 {
				constructor, method = "Entry-java.lang.String:A-", "put-java.lang.String-int:A-"
			}
			want := map[string][]member{
				"entry": {{Signature: "Entry(java.lang.String[])", Anchor: constructor}},
				"put":   {{Signature: "put(java.lang.String, int[])", Anchor: method}, {Signature: "put(int)", Anchor: "put(int)"}},
				"value": {{Signature: "VALUE", Anchor: "VALUE"}},
			}
			if version == 9 {
				want["put"][1].Anchor = "put-int-"
			}
			if !reflect.DeepEqual(typ.Members, want) {
				t.Fatalf("members = %#v; want %#v", typ.Members, want)
			}
		})
	}
}

func TestIndexBuildOverrides(t *testing.T) {
	root := "https://docs.oracle.com/en/java/javase/25/docs/api/"
	for _, tc := range []struct {
		name, module, page, oldPage, anchor, oldAnchor, wantPage, wantAnchor string
	}{
		{"explicit module", "java.explicit", "", "", "", "", "java.explicit/p/Outer.Inner.html", "%3Cinit%3E()"},
		{"new overrides win", "", "custom/New%20Type.html", "old.html", "%3Cinit%3E(java.lang.String%5B%5D)", "old-anchor", "custom/New%20Type.html", "%3Cinit%3E(java.lang.String%5B%5D)"},
		{"old overrides", "", "", "legacy/Outer.Inner.html", "", "custom%20anchor%23part%25", "legacy/Outer.Inner.html", "custom%20anchor%23part%25"},
		{"literal constructor override", "", "custom.html", "", "<init>()", "", "custom.html", "%3Cinit%3E()"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := buildIndex(25, root,
				[]indexEntry{{Package: "p", Label: "Outer.Inner", Module: tc.module, URL: tc.page, OldURL: tc.oldPage}},
				[]indexEntry{{Package: "p", Class: "Outer.Inner", Label: "Inner()", URL: tc.anchor, OldURL: tc.oldAnchor}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			typ := idx.Types["inner"][0]
			if typ.URL != root+tc.wantPage || typ.Members["inner"][0].Anchor != tc.wantAnchor {
				t.Fatalf("URL = %q, member = %+v", typ.URL, typ.Members["inner"])
			}
		})
	}
}

func TestIndexBuildRejectsIncompleteOrUnsafe(t *testing.T) {
	root := "https://docs.oracle.com/en/java/javase/25/docs/api/"
	for _, page := range []string{"https://evil.example/Type.html", "//evil.example/Type.html", "http://docs.oracle.com/en/java/javase/25/docs/api/T.html", "../T.html", "%2e%2e/T.html", "/elsewhere/T.html", "T.html?x=1", "T.html#member", "javascript:alert(1)", "%zz"} {
		t.Run(page, func(t *testing.T) {
			_, err := buildIndex(25, root, []indexEntry{{Package: "p", Label: "T", URL: page}}, []indexEntry{{Package: "p", Class: "T", Label: "f()"}}, nil)
			if err == nil {
				t.Errorf("accepted unsafe page %q", page)
			}
		})
	}
	for _, tc := range []struct {
		name    string
		types   []indexEntry
		members []indexEntry
	}{
		{"missing module", []indexEntry{{Package: "p", Label: "T"}}, []indexEntry{{Package: "p", Class: "T", Label: "f()"}}},
		{"no types", nil, []indexEntry{{Package: "p", Class: "T", Label: "f()"}}},
		{"synthetic only", []indexEntry{{Label: "All Classes", URL: "allclasses-index.html"}}, []indexEntry{{Label: "f()"}}},
		{"no members", []indexEntry{{Package: "p", Label: "T", Module: "m"}}, nil},
		{"unmatched members", []indexEntry{{Package: "p", Label: "T", Module: "m"}}, []indexEntry{{Package: "p", Class: "Other", Label: "f()"}}},
		{"invalid anchor escape", []indexEntry{{Package: "p", Label: "T", Module: "m"}}, []indexEntry{{Package: "p", Class: "T", Label: "f()", URL: "%zz"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			idx, err := buildIndex(25, root, tc.types, tc.members, nil)
			if err == nil || idx != nil {
				t.Fatalf("buildIndex = %+v, %v", idx, err)
			}
		})
	}
}

// Keep redirect handling identical to newLoader: following redirects hides probe failures.
func idxTestLoader(t *testing.T, handler http.HandlerFunc) *loader {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &loader{client: client, origin: srv.URL, retries: 2, backoff: time.Microsecond}
}

func TestIndexFetchStatusAndRedirects(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		location    string
		probe       bool
		unavailable bool
	}{
		{"probe 404", 404, "", true, true},
		{"required 404", 404, "", false, false},
		{"forbidden", 403, "", true, false},
		{"oracle landing", 302, "https://docs.oracle.com/en/java/javase/25/", true, true},
		{"oracle old landing", 301, "https://docs.oracle.com/javase/25/index.html", true, true},
		{"temporary landing", 307, "https://docs.oracle.com/en/java/javase/25/index.html", true, true},
		{"permanent landing", 308, "https://docs.oracle.com/javase/25/", true, true},
		{"required redirect", 302, "https://docs.oracle.com/en/java/javase/25/", false, false},
		{"same version", 302, "https://docs.oracle.com/en/java/javase/11/", true, false},
		{"arbitrary host", 302, "https://example.invalid/en/java/javase/25/", true, false},
		{"arbitrary oracle path", 302, "https://docs.oracle.com/error.html", true, false},
		{"http oracle", 302, "http://docs.oracle.com/en/java/javase/25/", true, false},
		{"query", 302, "https://docs.oracle.com/en/java/javase/25/?x=1", true, false},
		{"fragment", 302, "https://docs.oracle.com/en/java/javase/25/#x", true, false},
		{"other redirect status", 303, "https://docs.oracle.com/en/java/javase/25/", true, false},
		{"missing location", 302, "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/en/java/javase/11/docs/api/type-search-index.js" || r.Method != http.MethodGet || r.Header.Get("User-Agent") != "jdoc/1.0" {
					t.Errorf("unexpected request: %s %s, UA %q", r.Method, r.URL.Path, r.Header.Get("User-Agent"))
				}
				if tc.location != "" {
					w.Header().Set("Location", tc.location)
				}
				w.WriteHeader(tc.status)
			})
			data, err := l.fetch(context.Background(), 11, "type-search-index.js", tc.probe)
			if err == nil || errors.Is(err, errUnavailable) != tc.unavailable || data != nil {
				t.Fatalf("fetch = %q, %v; unavailable want %v", data, err, tc.unavailable)
			}
			if calls.Load() != 1 {
				t.Fatalf("made %d requests, want 1", calls.Load())
			}
		})
	}
}

func TestIndexFetchRetries(t *testing.T) {
	for _, status := range []int{500, 429} {
		for _, recover := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/recover=%v", status, recover), func(t *testing.T) {
				var calls atomic.Int32
				l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
					n := calls.Add(1)
					if !recover || n < 3 {
						w.WriteHeader(status)
						return
					}
					fmt.Fprint(w, "index bytes")
				})
				data, err := l.fetch(context.Background(), 9, "type-search-index.js", true)
				if calls.Load() != 3 || (err == nil) != recover || errors.Is(err, errUnavailable) {
					t.Fatalf("calls = %d, data = %q, err = %v", calls.Load(), data, err)
				}
				if recover && string(data) != "index bytes" {
					t.Fatalf("data = %q", data)
				}
				if !recover && !strings.Contains(err.Error(), fmt.Sprintf("HTTP %d", status)) {
					t.Fatalf("lost status: %v", err)
				}
			})
		}
	}
}

func TestIndexFetchCancellation(t *testing.T) {
	t.Run("before request", func(t *testing.T) {
		var calls atomic.Int32
		l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1) })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := l.fetch(ctx, 9, "type-search-index.js", true)
		if !errors.Is(err, context.Canceled) || calls.Load() != 0 {
			t.Fatalf("calls = %d, err = %v", calls.Load(), err)
		}
	})
	t.Run("in flight without retries", func(t *testing.T) {
		started := make(chan struct{})
		l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		})
		l.retries = 0
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := l.fetch(ctx, 9, "type-search-index.js", true); result <- err }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("request never arrived")
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("fetch = %v; want cancellation", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation did not interrupt request")
		}
	})
	t.Run("during backoff", func(t *testing.T) {
		var calls atomic.Int32
		attempted := make(chan struct{})
		l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				close(attempted)
			}
			w.WriteHeader(500)
		})
		l.backoff = time.Hour
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		result := make(chan error, 1)
		go func() { _, err := l.fetch(ctx, 9, "type-search-index.js", true); result <- err }()
		select {
		case <-attempted:
		case <-time.After(3 * time.Second):
			t.Fatal("request never arrived")
		}
		cancel()
		select {
		case err := <-result:
			if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
				t.Fatalf("calls = %d, err = %v", calls.Load(), err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancellation did not interrupt backoff")
		}
	})
}

func idxTestFixture(file string) string {
	switch file {
	case "type-search-index.js":
		return `typeSearchIndex=[{"p":"java.lang","l":"String"}];updateSearchResults();`
	case "member-search-index.js":
		return `memberSearchIndex=[{"p":"java.lang","c":"String","l":"length()"}];`
	case "package-search-index.js":
		return `packageSearchIndex=[{"l":"java.lang","m":"java.base"}];`
	}
	return ""
}

func TestIndexLoadReusesTypeProbe(t *testing.T) {
	for _, version := range []int{9, 10, 11} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			var mu sync.Mutex
			calls := make(map[string]int)
			l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				calls[r.URL.Path]++
				mu.Unlock()
				file := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				fmt.Fprint(w, idxTestFixture(file))
			})
			idx, err := l.load(context.Background(), version)
			if err != nil {
				t.Fatal(err)
			}
			if idx.Version != version || len(idx.Types["string"]) != 1 || len(idx.Types["string"][0].Members["length"]) != 1 {
				t.Fatalf("incomplete index: %+v", idx)
			}
			prefix := fmt.Sprintf("/javase/%d/docs/api/", version)
			if version >= 11 {
				prefix = "/en/java" + prefix
			}
			mu.Lock()
			defer mu.Unlock()
			want := map[string]int{prefix + "type-search-index.js": 1, prefix + "member-search-index.js": 1, prefix + "package-search-index.js": 1}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("requests = %v; want %v", calls, want)
			}
		})
	}
}

func TestIndexDiscoverSequentialAndDaily(t *testing.T) {
	var mu sync.Mutex
	calls := make(map[string]int)
	seen := make(map[int]int)
	l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
		var version int
		if _, err := fmt.Sscanf(r.URL.Path, "/javase/%d/docs/api/", &version); err != nil {
			_, _ = fmt.Sscanf(r.URL.Path, "/en/java/javase/%d/docs/api/", &version)
		}
		mu.Lock()
		calls[r.URL.Path]++
		if version == 10 && seen[9] != 3 || version == 11 && seen[10] != 3 {
			t.Errorf("version %d requested before preceding version finished: %v", version, seen)
		}
		seen[version]++
		mu.Unlock()
		if version == 11 {
			http.NotFound(w, r)
			return
		}
		if version != 9 && version != 10 {
			t.Errorf("unexpected version %d", version)
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, idxTestFixture(r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]))
	})
	c := &catalog{}
	if err := l.discover(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	latest, n := c.get(0)
	if latest == nil || n != 10 || latest.Version != 10 {
		t.Fatalf("latest = %+v, %d", latest, n)
	}
	for _, version := range []int{9, 10} {
		if idx, _ := c.get(version); idx == nil {
			t.Fatalf("version %d not published", version)
		}
	}
	if idx, _ := c.get(11); idx != nil {
		t.Fatal("unavailable version published")
	}
	mu.Lock()
	if len(calls) != 7 {
		t.Errorf("requests = %v; want six index files and one unavailable probe", calls)
	}
	for path, count := range calls {
		if count != 1 {
			t.Errorf("%s fetched %d times", path, count)
		}
	}
	before := make(map[string]int)
	for path, count := range calls {
		before[path] = count
	}
	mu.Unlock()
	if err := l.discover(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	before["/en/java/javase/11/docs/api/type-search-index.js"]++
	if !reflect.DeepEqual(calls, before) {
		t.Fatalf("daily discovery fetched more than latest+1: %v; want %v", calls, before)
	}
	if got, _ := c.get(0); got != latest {
		t.Fatal("daily probe replaced published latest index")
	}
}

func TestIndexDiscoverIncompleteNotPublished(t *testing.T) {
	for _, tc := range []struct {
		name, file, body string
		status           int
	}{
		{"type html", "type-search-index.js", `<html>error</html>`, 200},
		{"member missing", "member-search-index.js", "", 404},
		{"package missing", "package-search-index.js", "", 404},
		{"member malformed", "member-search-index.js", `memberSearchIndex=[`, 200},
		{"package empty", "package-search-index.js", `packageSearchIndex=[];`, 200},
		{"unmatched members", "member-search-index.js", `memberSearchIndex=[{"p":"other","c":"T","l":"f()"}];`, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := idxTestLoader(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/javase/10/docs/api/") {
					t.Errorf("discovery continued after incomplete version: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				file := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
				if file == tc.file {
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
					return
				}
				fmt.Fprint(w, idxTestFixture(file))
			})
			c := &catalog{}
			previous := &versionIndex{Version: 9, Types: map[string][]*javaType{}}
			c.publish(previous)
			err := l.discover(context.Background(), c)
			if err == nil || errors.Is(err, errUnavailable) || !strings.Contains(err.Error(), "JDK 10") {
				t.Fatalf("discover error = %v", err)
			}
			if idx, latest := c.get(0); idx != previous || latest != 9 {
				t.Fatalf("failed discovery changed catalog: %+v, %d", idx, latest)
			}
			if idx, _ := c.get(10); idx != nil {
				t.Fatal("incomplete index published")
			}
		})
	}
	t.Run("initial version unavailable is fatal", func(t *testing.T) {
		l := idxTestLoader(t, http.NotFound)
		c := &catalog{}
		if err := l.discover(context.Background(), c); !errors.Is(err, errUnavailable) {
			t.Fatalf("discover = %v", err)
		}
		if idx, latest := c.get(0); idx != nil || latest != 0 {
			t.Fatalf("empty catalog = %+v, %d", idx, latest)
		}
	})
}

func TestIndexCatalogConcurrentAccess(t *testing.T) {
	c := &catalog{}
	if idx, latest := c.get(0); idx != nil || latest != 0 {
		t.Fatalf("zero catalog = %+v, %d", idx, latest)
	}
	const versions = 64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start
			for version := 9 + worker; version < 9+versions; version += 8 {
				typ := &javaType{Name: "T", FQN: "p.T", URL: "https://example.invalid/T.html", Members: map[string][]member{"f": {{Signature: "f()", Anchor: "f()"}}}}
				c.publish(&versionIndex{Version: version, Types: map[string][]*javaType{"t": {typ}}})
			}
		}(worker)
	}
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			last := 0
			for i := 0; i < 1000; i++ {
				idx, latest := c.get(0)
				if latest < last || idx != nil && idx.Version != latest {
					t.Errorf("inconsistent latest: idx=%+v latest=%d previous=%d", idx, latest, last)
					return
				}
				last = latest
				if idx != nil && idx.Types["t"][0].Members["f"][0].Anchor != "f()" {
					t.Error("incomplete published index")
					return
				}
				if explicit, _ := c.get(9 + i%versions); explicit != nil && explicit.Version != 9+i%versions {
					t.Error("wrong explicit version")
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	for version := 9; version < 9+versions; version++ {
		if idx, latest := c.get(version); idx == nil || idx.Version != version || latest != 8+versions {
			t.Fatalf("version %d: idx=%+v latest=%d", version, idx, latest)
		}
	}
}

func TestIndexOverrideEscapingRoundTrip(t *testing.T) {
	for _, anchor := range []string{"%3Cinit%3E(java.lang.String%5B%5D)", "with%20space", "literal%25percent", "hash%23inside", "unicode-%E2%82%AC"} {
		idx, err := buildIndex(11, "https://docs.oracle.com/api/", []indexEntry{{Package: "p", Label: "T", Module: "m"}}, []indexEntry{{Package: "p", Class: "T", Label: "f()", URL: anchor}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		typ := idx.Types["t"][0]
		address, err := url.Parse(typ.URL + "#" + typ.Members["f"][0].Anchor)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := url.PathUnescape(anchor)
		if address.Fragment != want {
			t.Errorf("anchor %q round trips as %q; want %q", anchor, address.Fragment, want)
		}
	}
}
