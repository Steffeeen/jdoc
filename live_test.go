package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// Opt-in: keep routine tests independent of Oracle and network availability.
func TestLiveOracle(t *testing.T) {
	if os.Getenv("JDOC_LIVE") != "1" {
		t.Skip("set JDOC_LIVE=1 to discover and verify Oracle Javadocs")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	l := newLoader()
	c := &catalog{}
	if err := l.discover(ctx, c); err != nil {
		t.Fatal(err)
	}
	_, latest := c.get(0)
	runtime.GC()
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	t.Logf("JDK 9-%d retained heap: %.2f MiB", latest, float64(memory.HeapAlloc)/(1<<20))
	t.Logf("interned strings: %d", len(l.strings.ids))
	classCount := 0
	for _, variants := range l.classes {
		classCount += len(variants)
	}
	t.Logf("shared class records: %d", classCount)
	// Fingerprint all searchable destinations to compare index representations.
	digest := sha256.New()
	for version := 9; version <= latest; version++ {
		idx, _ := c.get(version)
		aliases := make([]string, 0, len(idx.Types))
		for alias := range idx.Types {
			aliases = append(aliases, idx.Strings.value(alias))
		}
		sort.Strings(aliases)
		for _, alias := range aliases {
			for _, match := range idx.search(query{Class: alias}) {
				fmt.Fprintf(digest, "%d %s %s %s\n", version, alias, match.Label, match.URL)
			}
			for _, typ := range idx.Types[l.strings.ids[alias]] {
				if alias != strings.ToLower(idx.Strings.value(typ.FQN)) {
					continue
				}
				names := make([]string, 0, len(typ.Members))
				for name := range typ.Members {
					names = append(names, idx.Strings.value(name))
				}
				sort.Strings(names)
				for _, name := range names {
					for _, match := range idx.search(query{Class: alias, Member: name}) {
						fmt.Fprintf(digest, "%d %s %s %s\n", version, name, match.Label, match.URL)
					}
				}
			}
		}
	}
	t.Logf("search destination SHA-256: %x", digest.Sum(nil))
	for version := 9; version <= latest; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			idx, _ := c.get(version)
			if idx == nil {
				t.Fatal("missing version")
			}
			foundArrayList := false
			for _, match := range idx.search(query{Class: "arrayl"}) {
				if match.Label == "java.util.ArrayList" {
					foundArrayList = true
				}
			}
			if !foundArrayList {
				t.Fatal("class prefix did not find java.util.ArrayList")
			}
			prefix := idx.search(query{Class: "arraylist", Member: "ad"})
			exact := idx.search(query{Class: "arraylist", Member: "add"})
			if len(exact) == 0 || len(prefix) <= len(exact) {
				t.Fatalf("ArrayList prefix should include more than the add overloads: prefix=%d exact=%d", len(prefix), len(exact))
			}
			for _, match := range prefix {
				if !strings.HasPrefix(strings.ToLower(match.Label), "java.util.arraylist#ad") {
					t.Errorf("unexpected prefix result: %s", match.Label)
				}
			}
			for _, match := range exact {
				if !strings.HasPrefix(match.Label, "java.util.ArrayList#add(") {
					t.Errorf("exact match expanded to other names: %s", match.Label)
				}
			}
			for _, input := range []string{
				"optionallong#orelse", "optionallong#orelseget", "map.entry",
				"java.util.arrayl#isem", "map.ent", "optional",
				"string#valueof(char[],int,int)", "string#substring(int,int)",
				"threadpoolexecutor.abortpolicy#abortpolicy()", "arrays#aslist", "collections#reverse",
			} {
				q, err := parseQuery(input)
				if err != nil {
					t.Fatal(err)
				}
				results := idx.search(q)
				if len(results) != 1 {
					t.Fatalf("%s: got %d results", input, len(results))
				}
				u, err := url.Parse(results[0].URL)
				if err != nil {
					t.Fatal(err)
				}
				fragment := u.Fragment
				u.Fragment, u.RawFragment = "", ""
				req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
				resp, err := l.client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 {
					t.Fatalf("GET %s: status=%d, error=%v", u, resp.StatusCode, err)
				}
				if fragment != "" && !strings.Contains(string(body), `id="`+fragment+`"`) && !strings.Contains(string(body), `name="`+fragment+`"`) && !strings.Contains(string(body), `id="`+html.EscapeString(fragment)+`"`) {
					t.Errorf("missing anchor %q in %s", fragment, u)
				}
			}
		})
	}
	runtime.KeepAlive(l)
}
