package main

import (
	"fmt"
	"html"
	"net/http"
	"time"
)

// NewWebServer serves /healthz and, when tokens is non-nil, the email check-in page.
// GET only renders a button and does not use up the token; the check-in itself is a
// POST, so link scanners and prefetchers cannot check you in by accident.
func NewWebServer(addr string, tokens *TokenStore, cmds Commands) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })

	if tokens != nil {
		mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("Content-Type", "text/html; charset=utf-8")
			h.Set("Cache-Control", "no-store")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("X-Robots-Tag", "noindex")
			switch r.Method {
			case http.MethodGet:
				t := r.URL.Query().Get("t")
				if !tokens.Valid(t) {
					expired(w)
					return
				}
				fmt.Fprintf(w, page, `<form method="post" action="/ok"><input type="hidden" name="t" value="`+
					html.EscapeString(t)+`"><button>✅ I'm OK</button></form>`)
			case http.MethodPost:
				if !tokens.Consume(r.FormValue("t")) {
					expired(w)
					return
				}
				fmt.Fprintf(w, page, "<p>"+html.EscapeString(cmds.CheckIn("email"))+"</p>")
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
		})
	}
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second}
}

func expired(w http.ResponseWriter) {
	w.WriteHeader(http.StatusGone)
	fmt.Fprintf(w, page, "<p>This link has expired or was already used.<br>Use the newest email or the Telegram bot.</p>")
}

const page = `<!doctype html><meta charset=utf-8><meta name=viewport content="width=device-width,initial-scale=1">
<title>Amialive</title><style>body{font:20px system-ui;display:grid;place-items:center;height:100vh;margin:0;text-align:center}
button{font:inherit;padding:1em 2em;border-radius:.5em;border:0;background:#2a7;color:#fff}</style>%s`
