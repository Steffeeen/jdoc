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
	table := &stringTable{}
	id := table.intern
	for _, version := range []int{17, 21} {
		root := fmt.Sprintf("https://docs.example/%d/", version)
		str := &javaType{FQN: id("java.lang.String"), Path: id("String.html"), Members: map[stringID][]member{
			id("length"): {{Signature: id("length()"), Anchor: id("length()")}},
			id("valueof"): {
				{Signature: id("valueOf(int)"), Anchor: id("valueOf(int)")},
				{Signature: id("valueOf(char[])"), Anchor: id("valueOf(char[])")},
			},
		}}
		idx := &versionIndex{Version: version, Root: root, Strings: table, Types: map[stringID][]*javaType{
			id("string"): {str}, id("java.lang.string"): {str},
			id("date"): {
				{FQN: id("java.util.Date"), Path: id("util/Date.html")},
				{FQN: id("java.sql.Date"), Path: id("sql/Date.html")},
			},
		}}
		if version == 17 {
			idx.Types[id("legacy")] = []*javaType{{FQN: id("example.Legacy"), Path: id("Legacy.html")}}
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
		`<!doctype html>`, `<html lang="en">`, `<main>`,
		`name="viewport" content="width=device-width, initial-scale=1"`,
		`<form action="/" method="get">`, `<label for="q">`, `id="q" name="q"`,
		`maxlength="1024" required`, `<button type="submit">`,
		`@media (max-width:500px)`, `flex-direction:column`, `:focus-visible`,
		`Current default: JDK 21.`,
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
		{"class prefix", "STR", 302, "https://docs.example/21/String.html", ""},
		{"qualified class prefix", "java.lang.str", 302, "https://docs.example/21/String.html", ""},
		{"pinned class prefix", "str 17", 302, "https://docs.example/17/String.html", ""},
		{"class and member prefixes", "str#len", 302, "https://docs.example/21/String.html#length()", ""},
		{"qualified class", "java.lang.String", 302, "https://docs.example/21/String.html", ""},
		{"unique method", "String#length", 302, "https://docs.example/21/String.html#length()", ""},
		{"unique member prefix", "String#LEN", 302, "https://docs.example/21/String.html#length()", ""},
		{"pinned prefix", "17 String#len", 302, "https://docs.example/17/String.html#length()", ""},
		{"signature", "String#valueOf( int )", 302, "https://docs.example/21/String.html#valueOf(int)", ""},
		{"prefix version", "17 String", 302, "https://docs.example/17/String.html", ""},
		{"suffix version", "String 17", 302, "https://docs.example/17/String.html", ""},
		{"prefix member version", "17 String#length()", 302, "https://docs.example/17/String.html#length()", ""},
		{"suffix member version", "String#length() 17", 302, "https://docs.example/17/String.html#length()", ""},
		{"ambiguous class", "Date", 200, "", `aria-label="Matching Javadocs"`},
		{"overloads", "String#valueOf", 200, "", "Multiple matches. Choose a reference below."},
		{"member prefix choices", "String#val", 200, "", "Multiple matches. Choose a reference below."},
		{"partial explicit signature", "String#val(int)", 404, "", "No match in this JDK."},
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
			links := regexp.MustCompile(`<li><a href="([^"]+)"(?: autofocus)?>([^<]+)</a></li>`).FindAllStringSubmatch(body, -1)
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

func TestServeHTTPFocusAndNavigation(t *testing.T) {
	for _, input := range []string{"", "Date", "String#valueOf", "Missing", "String#"} {
		t.Run(input, func(t *testing.T) {
			w := httptest.NewRecorder()
			httpTestCatalog().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?q="+url.QueryEscape(input), nil))
			body := w.Body.String()
			inputFocused := regexp.MustCompile(`<input[^>]* autofocus[^>]*>`).MatchString(body)
			if inputFocused != (input == "") {
				t.Errorf("search autofocus = %v for query %q", inputFocused, input)
			}
			results := regexp.MustCompile(`<li><a[^>]* autofocus>`).FindAllString(body, -1)
			ambiguous := input == "Date" || input == "String#valueOf"
			if !ambiguous {
				if len(results) != 0 || strings.Contains(body, "<script") {
					t.Error("non-result page includes result focus or navigation script")
				}
				return
			}
			if len(results) != 1 || !strings.Contains(body, `<ul id="results" aria-label="Matching Javadocs"><li><a href=`) {
				t.Fatal("expected exactly one focused result in navigation list")
			}
			first := regexp.MustCompile(`<ul id="results"[^>]*><li><a[^>]* autofocus>`)
			if !first.MatchString(body) {
				t.Error("first result does not receive autofocus")
			}
			script := regexp.MustCompile(`<script nonce="([^"]+)">`).FindStringSubmatch(body)
			if len(script) != 2 || !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'nonce-"+html.UnescapeString(script[1])+"'") {
				t.Fatal("navigation script must have a nonce authorized by CSP")
			}
			for _, key := range []string{"ArrowDown", "ArrowUp", "Home", "End"} {
				if !strings.Contains(body, "case '"+key+"':") {
					t.Errorf("missing keyboard handler for %s", key)
				}
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
