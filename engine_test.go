package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

type fakeUser struct{ asks, tells []string }

func (f *fakeUser) Ask(s string) error  { f.asks = append(f.asks, s); return nil }
func (f *fakeUser) Tell(s string) error { f.tells = append(f.tells, s); return nil }

type fakeAlert struct {
	subjects []string
	fail     bool
}

func (f *fakeAlert) Alert(c Contact, m Message) error {
	if f.fail {
		return errors.New("boom")
	}
	f.subjects = append(f.subjects, c.Name+":"+m.Subject)
	return nil
}

func (f *fakeAlert) count(substr string) (n int) {
	for _, s := range f.subjects {
		if strings.Contains(s, substr) {
			n++
		}
	}
	return
}

var day0 = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

func at(day, h, m int) time.Time {
	return day0.AddDate(0, 0, day).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

func setup(t *testing.T, missed int) (*Engine, *fakeUser, *fakeAlert) {
	t.Helper()
	cfg := &Config{Name: "Rob", Loc: time.UTC, CheckinHour: 9, Grace: 4 * time.Hour, Missed: missed,
		ReminderEvery: time.Hour, DataDir: t.TempDir()}
	u, a := &fakeUser{}, &fakeAlert{}
	e, err := NewEngine(cfg, []Contact{{Name: "Ann", Email: "a@x"}, {Name: "Bob", Phone: "+15550001111"}},
		[]UserChannel{u}, a, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return e, u, a
}

// run ticks once a minute from..to inclusive.
func run(e *Engine, from, to time.Time) {
	for t := from; !t.After(to); t = t.Add(time.Minute) {
		e.Tick(t)
	}
}

func TestPromptRemindAlertAllClear(t *testing.T) {
	e, u, a := setup(t, 1)
	run(e, at(0, 8, 0), at(0, 8, 59))
	if len(u.asks) != 0 {
		t.Fatalf("prompted too early: %v", u.asks)
	}
	run(e, at(0, 9, 0), at(0, 12, 59))
	if len(u.asks) != 4 { // prompt at 9 + reminders at 10, 11, 12
		t.Fatalf("want 4 asks, got %d: %v", len(u.asks), u.asks)
	}
	if len(a.subjects) != 0 {
		t.Fatal("alerted before grace elapsed")
	}
	run(e, at(0, 13, 0), at(0, 14, 0))
	if a.count("ALERT") != 2 {
		t.Fatalf("want 1 alert per contact (2), got %v", a.subjects)
	}
	e.checkIn(at(0, 14, 5), "test")
	if a.count("ALL CLEAR") != 2 {
		t.Fatalf("want all-clear to both contacts, got %v", a.subjects)
	}
	// Next day prompts again and doesn't alert immediately.
	run(e, at(0, 14, 6), at(1, 9, 30))
	if a.count("ALERT") != 2 {
		t.Fatalf("unexpected extra alert: %v", a.subjects)
	}
}

func TestCheckInBeforeDeadlinePreventsAlert(t *testing.T) {
	e, _, a := setup(t, 1)
	run(e, at(0, 9, 0), at(0, 10, 0))
	e.checkIn(at(0, 10, 1), "test")
	run(e, at(0, 10, 2), at(1, 8, 0))
	if len(a.subjects) != 0 {
		t.Fatalf("got alert after check-in: %v", a.subjects)
	}
}

func TestTwoMissedRequired(t *testing.T) {
	e, _, a := setup(t, 2)
	run(e, at(0, 9, 0), at(1, 12, 59))
	if len(a.subjects) != 0 {
		t.Fatalf("alerted after only one missed day: %v", a.subjects)
	}
	run(e, at(1, 13, 0), at(1, 13, 1))
	if a.count("ALERT") != 2 {
		t.Fatalf("want alert after 2nd missed day, got %v", a.subjects)
	}
}

func TestPause(t *testing.T) {
	e, u, a := setup(t, 1)
	e.st.PausedUntil = at(3, 0, 0)
	run(e, at(0, 9, 0), at(2, 23, 59))
	if len(u.asks) != 0 || len(a.subjects) != 0 {
		t.Fatalf("activity while paused: %v %v", u.asks, a.subjects)
	}
	run(e, at(3, 0, 0), at(3, 9, 0))
	if len(u.asks) != 1 {
		t.Fatalf("want a prompt after pause ends, got %v", u.asks)
	}
}

func TestDowntimeDoesNotCauseFalseAlarm(t *testing.T) {
	e, u, a := setup(t, 1)
	run(e, at(0, 9, 0), at(0, 9, 10))
	// Process dies, comes back after the grace period would have expired.
	e.Tick(at(0, 20, 0))
	if len(a.subjects) != 0 {
		t.Fatalf("false alarm after downtime: %v", a.subjects)
	}
	if !strings.Contains(u.asks[len(u.asks)-1], "offline") {
		t.Fatalf("expected offline notice, got %v", u.asks)
	}
	run(e, at(0, 20, 1), at(1, 0, 1))
	if a.count("ALERT") != 2 {
		t.Fatalf("want alert 4h after restart, got %v", a.subjects)
	}
}

func TestAlertRetriesOnFailure(t *testing.T) {
	e, _, a := setup(t, 1)
	a.fail = true
	run(e, at(0, 9, 0), at(0, 13, 5))
	if len(a.subjects) != 0 {
		t.Fatal("should not have delivered")
	}
	a.fail = false
	run(e, at(0, 13, 6), at(0, 13, 8))
	if a.count("ALERT") != 2 {
		t.Fatalf("want retry to succeed for both contacts, got %v", a.subjects)
	}
	run(e, at(0, 13, 9), at(0, 14, 0))
	if a.count("ALERT") != 2 {
		t.Fatalf("alert repeated: %v", a.subjects)
	}
}

func TestStatePersists(t *testing.T) {
	e, _, _ := setup(t, 1)
	run(e, at(0, 9, 0), at(0, 9, 1))
	e2, err := NewEngine(e.cfg, e.contacts, nil, &fakeAlert{}, e.log)
	if err != nil {
		t.Fatal(err)
	}
	if len(e2.st.Pending) != 1 || e2.st.LastPromptDay != "2026-01-05" {
		t.Fatalf("state not restored: %+v", e2.st)
	}
}
