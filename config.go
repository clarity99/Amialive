package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // embed tz database; the runtime image has none
)

// Contact is an emergency contact notified when a check-in is missed.
type Contact struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	Phone string `json:"phone,omitempty"` // E.164, e.g. +15551234567
}

func (c Contact) key() string { return c.Name + "|" + c.Email + "|" + c.Phone }

// Config holds all settings, loaded from environment variables.
type Config struct {
	Name          string
	Loc           *time.Location
	CheckinHour   int
	CheckinMinute int
	Grace         time.Duration // time allowed to answer a prompt before it counts as missed
	Missed        int           // consecutive missed check-ins before contacts are alerted
	ReminderEvery time.Duration // re-ping interval while a prompt is unanswered (0 = off)
	DataDir       string
	ContactsFile  string

	TelegramToken  string
	TelegramChatID int64

	SMTPHost, SMTPPort, SMTPUser, SMTPPassword, SMTPFrom string
	UserEmail                                            string

	TwilioSID, TwilioToken, TwilioFrom string

	HTTPAddr  string
	BaseURL   string
	Secret    string
	Heartbeat string
}

var phoneRE = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func LoadConfig() (*Config, []Contact, error) {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	c := &Config{
		Name:          env("AMIALIVE_NAME", "Your friend"),
		DataDir:       env("DATA_DIR", "/data"),
		ContactsFile:  env("CONTACTS_FILE", "/config/contacts.json"),
		TelegramToken: env("TELEGRAM_BOT_TOKEN", ""),
		SMTPHost:      env("SMTP_HOST", ""),
		SMTPPort:      env("SMTP_PORT", "587"),
		SMTPUser:      env("SMTP_USER", ""),
		SMTPPassword:  env("SMTP_PASSWORD", ""),
		SMTPFrom:      env("SMTP_FROM", ""),
		UserEmail:     env("USER_EMAIL", ""),
		TwilioSID:     env("TWILIO_ACCOUNT_SID", ""),
		TwilioToken:   env("TWILIO_AUTH_TOKEN", ""),
		TwilioFrom:    env("TWILIO_FROM", ""),
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		BaseURL:       strings.TrimRight(env("BASE_URL", ""), "/"),
		Secret:        env("CHECKIN_SECRET", ""),
		Heartbeat:     env("HEARTBEAT_URL", ""),
	}

	var err error
	if c.Loc, err = time.LoadLocation(env("TZ", "UTC")); err != nil {
		bad("TZ: %v", err)
		c.Loc = time.UTC
	}
	if t, err := time.Parse("15:04", env("CHECKIN_TIME", "09:00")); err != nil {
		bad("CHECKIN_TIME must be HH:MM: %v", err)
	} else {
		c.CheckinHour, c.CheckinMinute = t.Hour(), t.Minute()
	}
	if c.Grace, err = time.ParseDuration(env("GRACE_PERIOD", "4h")); err != nil || c.Grace < 0 {
		bad("GRACE_PERIOD must be a duration like 4h: %v", err)
	}
	if c.ReminderEvery, err = time.ParseDuration(env("REMINDER_INTERVAL", "1h")); err != nil || c.ReminderEvery < 0 {
		bad("REMINDER_INTERVAL must be a duration like 1h (0 disables): %v", err)
	}
	if c.Missed, err = strconv.Atoi(env("MISSED_CHECKINS", "1")); err != nil || c.Missed < 1 {
		bad("MISSED_CHECKINS must be an integer >= 1")
	}
	if v := env("TELEGRAM_CHAT_ID", ""); v != "" {
		if c.TelegramChatID, err = strconv.ParseInt(v, 10, 64); err != nil {
			bad("TELEGRAM_CHAT_ID must be an integer")
		}
	}
	if c.BaseURL != "" && len(c.Secret) < 16 {
		bad("CHECKIN_SECRET (>=16 chars) is required when BASE_URL is set")
	}

	contacts, err := loadContacts(c.ContactsFile)
	if err != nil {
		bad("%v", err)
	}
	return c, contacts, errors.Join(errs...)
}

func loadContacts(path string) ([]Contact, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("contacts file: %w", err)
	}
	var cs []Contact
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, fmt.Errorf("contacts file %s: %w", path, err)
	}
	if len(cs) == 0 {
		return nil, fmt.Errorf("contacts file %s: no contacts", path)
	}
	for _, c := range cs {
		if c.Name == "" || (c.Email == "" && c.Phone == "") {
			return nil, fmt.Errorf("contact %+v needs a name and an email and/or phone", c)
		}
		if c.Phone != "" && !phoneRE.MatchString(c.Phone) {
			return nil, fmt.Errorf("contact %q: phone must be E.164 (+15551234567)", c.Name)
		}
	}
	return cs, nil
}
