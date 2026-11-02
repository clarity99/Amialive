package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTS(t *testing.T) *TokenStore {
	ts, err := NewTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestTokenLifecycle(t *testing.T) {
	ts := newTS(t)
	a, _ := ts.New()
	b, _ := ts.New()
	if a == b || len(a) < 40 {
		t.Fatal("tokens must be unique and long")
	}
	if !ts.Valid(a) || !ts.Valid(a) {
		t.Fatal("Valid must not consume")
	}
	if !ts.Consume(a) || ts.Consume(a) {
		t.Fatal("token must be single-use")
	}
	if ts.Consume("") || ts.Consume("nope") {
		t.Fatal("bogus token accepted")
	}
	ts.RevokeAll()
	if ts.Valid(b) {
		t.Fatal("RevokeAll left a token valid")
	}
}

func TestTokenExpiryAndPersistence(t *testing.T) {
	ts := newTS(t)
	tok, _ := ts.New()
	ts2, err := NewTokenStore(ts.path)
	if err != nil || !ts2.Valid(tok) {
		t.Fatalf("token lost across restart: %v", err)
	}
	ts2.now = func() time.Time { return time.Now().Add(tokenTTL + time.Minute) }
	if ts2.Valid(tok) {
		t.Fatal("expired token accepted")
	}
}

type fakeCmds struct{ checkins int }

func (f *fakeCmds) CheckIn(string) string { f.checkins++; return "thanks" }
func (f *fakeCmds) Status() string        { return "" }
func (f *fakeCmds) Pause(int) string      { return "" }
func (f *fakeCmds) Resume() string        { return "" }

func TestWebFlow(t *testing.T) {
	ts, cmds := newTS(t), &fakeCmds{}
	srv := httptest.NewServer(NewWebServer("", ts, cmds).Handler)
	defer srv.Close()
	tok, _ := ts.New()

	resp, _ := http.Get(srv.URL + "/ok?t=" + tok)
	if resp.StatusCode != 200 || cmds.checkins != 0 || !ts.Valid(tok) {
		t.Fatalf("GET must render only: status=%d checkins=%d", resp.StatusCode, cmds.checkins)
	}
	resp, _ = http.PostForm(srv.URL+"/ok", url.Values{"t": {tok}})
	if resp.StatusCode != 200 || cmds.checkins != 1 {
		t.Fatalf("POST should check in: status=%d", resp.StatusCode)
	}
	resp, _ = http.PostForm(srv.URL+"/ok", url.Values{"t": {tok}})
	if resp.StatusCode != http.StatusGone || cmds.checkins != 1 {
		t.Fatalf("replay must fail: status=%d checkins=%d", resp.StatusCode, cmds.checkins)
	}
	resp, _ = http.Get(srv.URL + "/ok?t=" + strings.Repeat("x", 43))
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("bogus token: %d", resp.StatusCode)
	}
}

func TestCheckInRevokesLinks(t *testing.T) {
	e, _, _ := setup(t, 1)
	ts := newTS(t)
	e.OnCheckIn = ts.RevokeAll
	tok, _ := ts.New()
	e.checkIn(at(0, 10, 0), "telegram")
	if ts.Valid(tok) {
		t.Fatal("email link survived a Telegram check-in")
	}
}
