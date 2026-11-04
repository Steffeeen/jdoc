package main

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
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
	for version := 9; version <= latest; version++ {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			idx, _ := c.get(version)
			if idx == nil {
				t.Fatal("missing version")
			}
			for _, input := range []string{
				"optionallong#orelse", "optionallong#orelseget", "map.entry",
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
}
