package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"sync"
	"time"
)

const (
	tokenTTL  = 48 * time.Hour // long enough for a late check-in the next morning
	maxTokens = 100
)

type tokenRec struct {
	Hash    string    `json:"hash"` // sha256 of the token; the token itself is never stored
	Expires time.Time `json:"expires"`
}

// TokenStore issues single-use check-in tokens for email links.
// Every token is revoked as soon as any check-in succeeds.
type TokenStore struct {
	mu   sync.Mutex
	path string
	recs []tokenRec
	now  func() time.Time
}

func NewTokenStore(path string) (*TokenStore, error) {
	ts := &TokenStore{path: path, now: time.Now}
	var f struct{ Tokens []tokenRec }
	if err := loadJSON(path, &f); err != nil {
		return nil, err
	}
	ts.recs = f.Tokens
	return ts, nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func (ts *TokenStore) save() error {
	return saveState(ts.path, struct{ Tokens []tokenRec }{ts.recs})
}

func (ts *TokenStore) prune() {
	keep := ts.recs[:0]
	for _, r := range ts.recs {
		if ts.now().Before(r.Expires) {
			keep = append(keep, r)
		}
	}
	ts.recs = keep
	if len(ts.recs) > maxTokens {
		ts.recs = ts.recs[len(ts.recs)-maxTokens:]
	}
}

// New returns a fresh random token (256 bits).
func (ts *TokenStore) New() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	ts.prune()
	ts.recs = append(ts.recs, tokenRec{hashToken(tok), ts.now().Add(tokenTTL)})
	return tok, ts.save()
}

func (ts *TokenStore) find(tok string) int {
	h := hashToken(tok)
	for i, r := range ts.recs {
		if subtle.ConstantTimeCompare([]byte(h), []byte(r.Hash)) == 1 && ts.now().Before(r.Expires) {
			return i
		}
	}
	return -1
}

// Valid reports whether tok is usable, without consuming it.
func (ts *TokenStore) Valid(tok string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return tok != "" && ts.find(tok) >= 0
}

// Consume uses tok up. It returns false if the token is unknown, expired or already used.
func (ts *TokenStore) Consume(tok string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	i := -1
	if tok != "" {
		i = ts.find(tok)
	}
	if i < 0 {
		return false
	}
	ts.recs = append(ts.recs[:i], ts.recs[i+1:]...)
	_ = ts.save()
	return true
}

// RevokeAll invalidates every outstanding link.
func (ts *TokenStore) RevokeAll() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.recs) == 0 {
		return
	}
	ts.recs = nil
	_ = ts.save()
}
