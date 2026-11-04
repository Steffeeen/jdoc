package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
)

//go:embed search.html
var pageHTML string

var page = template.Must(template.New("search").Parse(pageHTML))

type pageData struct {
	Query   string
	Latest  int
	Message string
	Results []result
	Nonce   string
}

func (c *catalog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' https://docs.oracle.com; base-uri 'none'; frame-ancestors 'none'")
	_, latest := c.get(0)
	data := pageData{Query: r.URL.Query().Get("q"), Latest: latest}
	status := http.StatusOK
	if data.Query != "" {
		q, err := parseQuery(data.Query)
		if err != nil {
			data.Message, status = err.Error(), http.StatusBadRequest
		} else if idx, _ := c.get(q.Version); idx == nil {
			data.Message, status = "That JDK version is not indexed.", http.StatusNotFound
		} else {
			data.Results = idx.search(q)
			switch len(data.Results) {
			case 0:
				data.Message, status = "No match in this JDK. Try a different name or an explicit version.", http.StatusNotFound
			case 1:
				http.Redirect(w, r, data.Results[0].URL, http.StatusFound)
				return
			default:
				data.Message = "Multiple matches. Choose a reference below."
			}
		}
	}
	if len(data.Results) > 0 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			slog.Error("generate script nonce", "error", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		data.Nonce = base64.StdEncoding.EncodeToString(nonce[:])
		w.Header().Set("Content-Security-Policy", w.Header().Get("Content-Security-Policy")+"; script-src 'nonce-"+data.Nonce+"'")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if r.Method == http.MethodHead {
		return
	}
	if err := page.Execute(w, data); err != nil {
		slog.Error("render search page", "error", err)
	}
}
