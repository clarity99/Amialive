package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// State is persisted to disk so restarts don't lose track of pending check-ins.
type State struct {
	LastCheckIn   time.Time       `json:"last_check_in"`
	LastPromptDay string          `json:"last_prompt_day"`
	LastReminder  time.Time       `json:"last_reminder"`
	Pending       []time.Time     `json:"pending"` // unanswered prompt times, oldest first
	GraceResetAt  time.Time       `json:"grace_reset_at"`
	PausedUntil   time.Time       `json:"paused_until"`
	Alerted       bool            `json:"alerted"`
	AlertedAt     time.Time       `json:"alerted_at"`
	Notified      map[string]bool `json:"notified"`
	LastTick      time.Time       `json:"last_tick"`
}

func loadState(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(b, &s)
}

func saveState(path string, s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
