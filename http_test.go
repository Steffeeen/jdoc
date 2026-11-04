package main

import (
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

func httpTestCatalog() *catalog {
	c := &catalog{latest: 21, versions: make(map[int]*versionIndex)}
	for _, version := range []int{17, 21} {
		root := fmt.Sprintf("https://docs.example/%d/", version)
		str := &javaType{FQN: "java.lang.String", URL: root + "String.html", Members: map[string][]member{
			"length": {{Signature: "length()", Anchor: "length()"}},
			"valueof": {
				{Signature: "valueOf(int)", Anchor: "valueOf(int)"},
				{Signature: "valueOf(char[])", Anchor: "valueOf(char[])"},
			},
		}}
		idx := &versionIndex{Version: version, Types: map[string][]*javaType{
			"string": {str}, "java.lang.string": {str},
			"date": {
				{FQN: "java.util.Date", URL: root + "util/Date.html"},
				{FQN: "java.sql.Date", URL: root + "sql/Date.html"},
			},
		}}
		if version == 17 {
			idx.Types["legacy"] = []*javaType{{FQN: "example.Legacy", URL: root + "Legacy.html"}}
		}
		c.versions[version] = idx
	}
	return c
}

func TestServeHTTPHome(t *testing.T) {
	w := httptest.NewRecorder()
	httpTestCatalog().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d; want 200", w.Code)
	}
	for key, want := range map[string]string{
		"Content-Type": "text/html; charset=utf-8", "Cache-Control": "no-store",
		"X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://docs.oracle.com; base-uri 'none'; frame-ancestors 'none'",
	} {
		if got := w.Header().Get(key); got != want {
			t.Errorf("%s = %q; want %q", key, got, want)
		}
	}
	body := w.Body.String()
	for _, want := range []string{
		`<!doctype html>`, `<html lang="en">`, `<main>`, `<h1>`,
		`name="viewport" content="width=device-width, initial-scale=1"`,
		`<form action="/" method="get">`, `<label for="q">`, `id="q" name="q"`,
		`maxlength="1024" required`, `<button type="submit">`,
		`@media (max-width:500px)`, `flex-direction:column`, `:focus-visible`,
		`Default: JDK 21.`, `Explicit versions stay pinned.`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing %q", want)
		}
	}
}

func TestServeHTTPSearch(t *testing.T) {
	tests := []struct {
		name, input string
		status      int
		location    string
		contains    string
	}{
		{"latest class", "STRING", 302, "https://docs.example/21/String.html", ""},
		{"qualified class", "java.lang.String", 302, "https://docs.example/21/String.html", ""},
		{"unique method", "String#length", 302, "https://docs.example/21/String.html#length()", ""},
		{"signature", "String#valueOf( int )", 302, "https://docs.example/21/String.html#valueOf(int)", ""},
		{"prefix version", "17 String", 302, "https://docs.example/17/String.html", ""},
		{"suffix version", "String 17", 302, "https://docs.example/17/String.html", ""},
		{"prefix member version", "17 String#length()", 302, "https://docs.example/17/String.html#length()", ""},
		{"suffix member version", "String#length() 17", 302, "https://docs.example/17/String.html#length()", ""},
		{"ambiguous class", "Date", 200, "", `aria-label="Matching Javadocs"`},
		{"overloads", "String#valueOf", 200, "", "Multiple matches. Choose a reference below."},
		{"unknown class", "Missing", 404, "", "No match in this JDK."},
		{"unknown member", "String#missing", 404, "", "No match in this JDK."},
		{"unknown signature", "String#valueOf(long)", 404, "", "No match in this JDK."},
		{"no older fallback", "Legacy", 404, "", "No match in this JDK."},
		{"explicit older match", "Legacy 17", 302, "https://docs.example/17/Legacy.html", ""},
		{"unindexed version", "19 String", 404, "", "That JDK version is not indexed."},
		{"future version", "String 99", 404, "", "That JDK version is not indexed."},
		{"invalid", "String#", 400, "", "enter a member name after #"},
		{"old version", "8 String", 400, "", "JDK versions must be integers starting at 9"},
		{"duplicate version", "17 String 21", 400, "", "specify a JDK version only once"},
		{"too long", strings.Repeat("a", 1025), 400, "", "enter a class or class#member"},
		{"blank", " \t", 400, "", "enter a class or class#member"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				t.Run(method, func(t *testing.T) {
					w := httptest.NewRecorder()
					httpTestCatalog().ServeHTTP(w, httptest.NewRequest(method, "/?q="+url.QueryEscape(tt.input), nil))
					if w.Code != tt.status || w.Header().Get("Location") != tt.location {
						t.Fatalf("status/location = %d/%q; want %d/%q", w.Code, w.Header().Get("Location"), tt.status, tt.location)
					}
					if method == http.MethodHead {
						if w.Body.Len() != 0 {
							t.Errorf("HEAD wrote %d body bytes", w.Body.Len())
						}
					} else if !strings.Contains(w.Body.String(), tt.contains) {
						t.Errorf("body missing %q: %s", tt.contains, w.Body.String())
					}
				})
			}
		})
	}
}

func TestServeHTTPAmbiguousResults(t *testing.T) {
	for _, input := range []string{"Date", "String#valueOf"} {
		t.Run(input, func(t *testing.T) {
			w := httptest.NewRecorder()
			c := httpTestCatalog()
			c.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?q="+url.QueryEscape(input), nil))
			q, err := parseQuery(input)
			if err != nil {
				t.Fatal(err)
			}
			body := w.Body.String()
			links := regexp.MustCompile(`<li><a href="([^"]+)">([^<]+)</a></li>`).FindAllStringSubmatch(body, -1)
			matches := c.versions[21].search(q)
			if len(links) != len(matches) || len(links) != 2 {
				t.Fatalf("got %d links; want 2", len(links))
			}
			for i, match := range matches {
				address, err := url.PathUnescape(html.UnescapeString(links[i][1]))
				if err != nil || address != match.URL || html.UnescapeString(links[i][2]) != match.Label {
					t.Errorf("link %d = %q, %q (%v); want %#v", i, address, links[i][2], err, match)
				}
			}
			if !strings.Contains(body, `role="status"`) {
				t.Error("ambiguity message lacks accessible status role")
			}
		})
	}
}

func TestServeHTTPRouting(t *testing.T) {
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodPost, "/", 405}, {http.MethodPut, "/", 405},
		{http.MethodDelete, "/", 405}, {http.MethodOptions, "/", 405},
		{http.MethodGet, "/missing", 404}, {http.MethodGet, "/favicon.ico", 404},
		{http.MethodPost, "/missing", 404}, {http.MethodHead, "/", 200},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			httpTestCatalog().ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.status {
				t.Errorf("status = %d; want %d", w.Code, tt.status)
			}
			if tt.status == 405 && w.Header().Get("Allow") != "GET, HEAD" {
				t.Errorf("Allow = %q; want GET, HEAD", w.Header().Get("Allow"))
			}
			if tt.method == http.MethodHead && w.Body.Len() != 0 {
				t.Errorf("HEAD wrote %d body bytes", w.Body.Len())
			}
		})
	}
}

func TestServeHTTPEscapesQuery(t *testing.T) {
	input := `"><script>alert('x')</script>&`
	w := httptest.NewRecorder()
	httpTestCatalog().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?q="+url.QueryEscape(input), nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, input) || strings.Contains(body, "<script>") {
		t.Fatalf("unescaped query in body: %s", body)
	}
	if !strings.Contains(body, `value="`+html.EscapeString(input)+`"`) {
		t.Errorf("escaped query missing from input: %s", body)
	}
}
