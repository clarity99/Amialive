package main

import (
	"crypto/subtle"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"time"
)

// NewWebServer serves /healthz and, when a secret is set, the email check-in page.
// GET only renders a button; the check-in itself is a POST, so link scanners and
// prefetchers cannot check you in by accident.
func NewWebServer(addr, secret string, cmds Commands) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })

	if secret != "" {
		mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
			k := r.URL.Query().Get("k")
			if r.Method == http.MethodPost {
				k = r.FormValue("k")
			}
			if subtle.ConstantTimeCompare([]byte(k), []byte(secret)) != 1 {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			if r.Method == http.MethodPost {
				fmt.Fprintf(w, page, "<p>"+html.EscapeString(cmds.CheckIn("web"))+"</p>")
				return
			}
			fmt.Fprintf(w, page, `<form method="post" action="/ok"><input type="hidden" name="k" value="`+
				html.EscapeString(k)+`"><button>✅ I'm OK</button></form>`)
		})
	}
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second}
}

func checkinLink(base, secret string) string { return base + "/ok?k=" + url.QueryEscape(secret) }

const page = `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">
<title>Amialive</title><style>body{font:20px system-ui;display:grid;place-items:center;height:100vh;margin:0}
button{font:inherit;padding:1em 2em;border-radius:.5em;border:0;background:#2a7;color:#fff}</style>%s`
