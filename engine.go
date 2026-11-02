package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	downtimeGap   = 15 * time.Minute // a longer gap between ticks means we were offline/asleep
	tickSaveEvery = 5 * time.Minute  // persist LastTick this often (spares SD cards)
	maxPending    = 60
)

// UserChannel is a way of reaching the monitored person.
type UserChannel interface {
	Ask(text string) error  // prompt for a check-in
	Tell(text string) error // informational message
}

// Message is what contacts receive; each channel uses the relevant field.
type Message struct{ Subject, Body, SMS string }

// AlertSender delivers a message to a contact over any available channel.
// It returns nil if at least one channel succeeded.
type AlertSender interface {
	Alert(c Contact, m Message) error
}

type Engine struct {
	mu       sync.Mutex
	cfg      *Config
	contacts []Contact
	st       State
	path     string
	users    []UserChannel
	alert    AlertSender
	log      *slog.Logger

	// OnCheckIn, if set, runs after every successful check-in (used to revoke email links).
	OnCheckIn func()

	dirty         bool
	lastSavedTick time.Time
}

func NewEngine(cfg *Config, contacts []Contact, users []UserChannel, alert AlertSender, log *slog.Logger) (*Engine, error) {
	path := filepath.Join(cfg.DataDir, "state.json")
	st, err := loadState(path)
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}
	return &Engine{cfg: cfg, contacts: contacts, st: st, path: path, users: users, alert: alert, log: log}, nil
}

func (e *Engine) ask(text string) {
	for _, u := range e.users {
		if err := u.Ask(text); err != nil {
			e.log.Error("prompt failed", "err", err)
		}
	}
}

func (e *Engine) tell(text string) {
	for _, u := range e.users {
		if err := u.Tell(text); err != nil {
			e.log.Error("notify failed", "err", err)
		}
	}
}

func (e *Engine) fmtTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.In(e.cfg.Loc).Format("Mon 2 Jan 15:04 MST")
}

func (e *Engine) due(now time.Time) time.Time {
	n := now.In(e.cfg.Loc)
	return time.Date(n.Year(), n.Month(), n.Day(), e.cfg.CheckinHour, e.cfg.CheckinMinute, 0, 0, e.cfg.Loc)
}

// deadline is when contacts get alerted, given the current pending prompts.
func (e *Engine) deadline() time.Time {
	base := e.st.Pending[e.cfg.Missed-1]
	if e.st.GraceResetAt.After(base) {
		base = e.st.GraceResetAt
	}
	return base.Add(e.cfg.Grace)
}

// Tick advances the state machine. Call it about once a minute.
func (e *Engine) Tick(now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.flush(now)
	s := &e.st

	gap := now.Sub(s.LastTick)
	offline := !s.LastTick.IsZero() && gap > downtimeGap
	s.LastTick = now
	if now.Sub(e.lastSavedTick) >= tickSaveEvery {
		e.dirty = true
	}

	if !s.PausedUntil.IsZero() {
		if now.Before(s.PausedUntil) {
			return
		}
		s.PausedUntil, s.Pending, s.GraceResetAt = time.Time{}, nil, time.Time{}
		s.LastPromptDay = "" // prompt again today if past check-in time
		e.dirty = true
		e.tell("Pause is over, check-ins are active again.")
	}

	// We were not running, so don't blame the user for prompts we couldn't deliver
	// or reminders we couldn't send: restart the grace period.
	if offline && len(s.Pending) > 0 {
		s.GraceResetAt = now
		s.LastReminder = now
		e.dirty = true
		e.log.Warn("detected downtime, restarting grace period", "gap", gap)
		e.ask(fmt.Sprintf("I was offline for a while. Are you OK? Please check in before %s.", e.fmtTime(e.deadline())))
	}

	day := now.In(e.cfg.Loc).Format("2006-01-02")
	switch {
	case !now.Before(e.due(now)) && s.LastPromptDay != day:
		s.LastPromptDay = day
		s.LastReminder = now
		if len(s.Pending) < maxPending {
			s.Pending = append(s.Pending, now)
		}
		e.dirty = true
		msg := "Are you OK? Please check in."
		if len(s.Pending) >= e.cfg.Missed && !s.Alerted {
			msg += fmt.Sprintf(" Your emergency contacts will be notified at %s if I don't hear from you.", e.fmtTime(e.deadline()))
		}
		e.ask(msg)
	case len(s.Pending) > 0 && !s.Alerted && e.cfg.ReminderEvery > 0 && now.Sub(s.LastReminder) >= e.cfg.ReminderEvery:
		s.LastReminder = now
		e.dirty = true
		msg := "Reminder: I still haven't heard from you."
		if len(s.Pending) >= e.cfg.Missed {
			msg += fmt.Sprintf(" Emergency contacts will be notified at %s.", e.fmtTime(e.deadline()))
		}
		e.ask(msg)
	}

	if len(s.Pending) >= e.cfg.Missed && !now.Before(e.deadline()) {
		e.raiseAlert(now)
	}
}

func (e *Engine) raiseAlert(now time.Time) {
	s := &e.st
	if !s.Alerted {
		s.Alerted, s.AlertedAt, s.Notified = true, now, map[string]bool{}
		e.dirty = true
		e.log.Warn("missed check-in, alerting emergency contacts", "pending", len(s.Pending))
	}
	m := Message{
		Subject: fmt.Sprintf("ALERT: %s missed their check-in", e.cfg.Name),
		Body: fmt.Sprintf("Hello,\n\n%s was asked to confirm they are OK but has not responded (last check-in: %s, %d missed).\n\n"+
			"Please try to contact them. If you cannot reach them, consider requesting a welfare check.\n\n"+
			"This is an automated message from Amialive.\n", e.cfg.Name, e.fmtTime(s.LastCheckIn), len(s.Pending)),
		SMS: fmt.Sprintf("[Amialive] %s missed their check-in (last OK: %s). Please try to reach them.", e.cfg.Name, e.fmtTime(s.LastCheckIn)),
	}
	failed := 0
	for _, c := range e.contacts {
		if s.Notified[c.key()] {
			continue
		}
		if err := e.alert.Alert(c, m); err != nil {
			failed++
			e.log.Error("alert failed, will retry", "contact", c.Name, "err", err)
			continue
		}
		s.Notified[c.key()] = true
		e.dirty = true
		e.log.Info("alert sent", "contact", c.Name)
	}
	if failed == 0 && s.AlertedAt.Equal(now) {
		e.tell(fmt.Sprintf("Emergency contacts have been notified (%d). Check in as soon as you can to send an all-clear.", len(e.contacts)))
	}
}

// CheckIn records that the user is OK. via is only used for logging.
func (e *Engine) CheckIn(via string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	return e.checkIn(now, via)
}

func (e *Engine) checkIn(now time.Time, via string) string {
	s := &e.st
	wasAlerted := s.Alerted
	notified := s.Notified
	s.LastCheckIn, s.Pending, s.GraceResetAt = now, nil, time.Time{}
	s.Alerted, s.Notified = false, nil
	e.dirty = true
	e.flush(now)
	e.log.Info("check-in", "via", via)
	if e.OnCheckIn != nil {
		e.OnCheckIn()
	}

	reply := "✅ Thanks, you're checked in. Next check-in: " + e.fmtTime(e.nextPrompt(now))
	if wasAlerted {
		m := Message{
			Subject: fmt.Sprintf("ALL CLEAR: %s has checked in", e.cfg.Name),
			Body:    fmt.Sprintf("%s has now checked in and is OK. No further action is needed.\n\nAmialive\n", e.cfg.Name),
			SMS:     fmt.Sprintf("[Amialive] All clear: %s has checked in and is OK.", e.cfg.Name),
		}
		n := 0
		for _, c := range e.contacts {
			if notified[c.key()] && e.alert.Alert(c, m) == nil {
				n++
			}
		}
		reply += fmt.Sprintf("\nAll-clear sent to %d contact(s).", n)
	}
	return reply
}

func (e *Engine) nextPrompt(now time.Time) time.Time {
	d := e.due(now)
	if e.st.LastPromptDay == now.In(e.cfg.Loc).Format("2006-01-02") || now.After(d) {
		d = e.due(now.Add(24 * time.Hour))
	}
	return d
}

// Pause suspends check-ins for the given number of days.
func (e *Engine) Pause(days int) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.st.PausedUntil = now.Add(time.Duration(days) * 24 * time.Hour)
	e.st.Pending, e.st.GraceResetAt = nil, time.Time{}
	e.dirty = true
	e.flush(now)
	return fmt.Sprintf("⏸ Paused until %s. No prompts or alerts until then. Use /resume to end early.", e.fmtTime(e.st.PausedUntil))
}

func (e *Engine) Resume() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	e.st.PausedUntil = time.Time{}
	e.st.LastPromptDay = ""
	e.dirty = true
	e.flush(now)
	return "▶️ Check-ins resumed."
}

func (e *Engine) Status() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	s := e.st
	var b strings.Builder
	fmt.Fprintf(&b, "Last check-in: %s\n", e.fmtTime(s.LastCheckIn))
	switch {
	case !s.PausedUntil.IsZero():
		fmt.Fprintf(&b, "Paused until %s\n", e.fmtTime(s.PausedUntil))
	case s.Alerted:
		fmt.Fprintf(&b, "🚨 Contacts were alerted at %s\n", e.fmtTime(s.AlertedAt))
	case len(s.Pending) > 0 && len(s.Pending) >= e.cfg.Missed:
		fmt.Fprintf(&b, "Awaiting check-in (%d unanswered). Alert at %s\n", len(s.Pending), e.fmtTime(e.deadline()))
	case len(s.Pending) > 0:
		fmt.Fprintf(&b, "%d unanswered (alert after %d missed)\n", len(s.Pending), e.cfg.Missed)
	default:
		fmt.Fprintf(&b, "Next prompt: %s\n", e.fmtTime(e.nextPrompt(now)))
	}
	fmt.Fprintf(&b, "Alert after %d missed check-in(s) + %s grace", e.cfg.Missed, e.cfg.Grace)
	return b.String()
}

func (e *Engine) flush(now time.Time) {
	if !e.dirty {
		return
	}
	if err := saveState(e.path, e.st); err != nil {
		e.log.Error("save state", "err", err)
		return
	}
	e.dirty, e.lastSavedTick = false, now
}
