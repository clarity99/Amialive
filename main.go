package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	testContacts := flag.Bool("test-contacts", false, "send a test message to every emergency contact and exit")
	healthcheck := flag.Bool("healthcheck", false, "probe the local HTTP server and exit (for Docker)")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if *healthcheck {
		addr := env("HTTP_ADDR", ":8080")
		if addr[0] == ':' {
			addr = "127.0.0.1" + addr
		}
		resp, err := httpClient.Get("http://" + addr + "/healthz")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}

	cfg, contacts, err := LoadConfig()
	if err != nil {
		log.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	mailer, sms := NewMailer(cfg), NewBulkGate(cfg)
	alerter := &Alerter{Mail: mailer, SMS: sms, Log: log}

	if *testContacts {
		m := Message{Subject: "Amialive test", Body: fmt.Sprintf("This is a test message from %s's Amialive. Nothing is wrong.\n", cfg.Name),
			SMS: fmt.Sprintf("[Amialive] Test message for %s's emergency contacts. Nothing is wrong.", cfg.Name)}
		failed := false
		for _, c := range contacts {
			if err := alerter.Alert(c, m); err != nil {
				log.Error("test failed", "contact", c.Name, "err", err)
				failed = true
			} else {
				log.Info("test sent", "contact", c.Name)
			}
		}
		if failed {
			os.Exit(1)
		}
		return
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Error("data dir", "err", err)
		os.Exit(1)
	}

	var users []UserChannel
	var bot *Bot
	engine, err := NewEngine(cfg, contacts, nil, alerter, log)
	if err != nil {
		log.Error("engine", "err", err)
		os.Exit(1)
	}
	if cfg.TelegramToken != "" {
		bot = NewBot(cfg.TelegramToken, cfg.TelegramChatID, engine, log)
		if cfg.TelegramChatID != 0 {
			users = append(users, bot)
		}
	}
	if cfg.UserEmail != "" && mailer != nil && cfg.BaseURL != "" {
		users = append(users, &EmailUser{Mail: mailer, To: cfg.UserEmail, Link: checkinLink(cfg.BaseURL, cfg.Secret)})
	}
	engine.users = users

	setupOnly := len(users) == 0
	if setupOnly && bot == nil {
		log.Error("no check-in channel configured: set TELEGRAM_BOT_TOKEN (+TELEGRAM_CHAT_ID) and/or USER_EMAIL+BASE_URL+CHECKIN_SECRET")
		os.Exit(2)
	}
	if setupOnly {
		log.Warn("TELEGRAM_CHAT_ID not set: setup mode. Send /start to the bot to learn your chat ID; no check-ins run until it is set.")
	} else if mailer == nil && sms == nil {
		log.Error("no alert channel configured: set SMTP_* and/or BULKGATE_*")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var wg sync.WaitGroup
	run := func(f func()) { wg.Add(1); go func() { defer wg.Done(); f() }() }

	srv := NewWebServer(cfg.HTTPAddr, secretIfEmail(cfg), engine)
	run(func() {
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "err", err)
		}
	})
	if bot != nil {
		run(func() { bot.Run(ctx) })
	}
	if !setupOnly {
		run(func() {
			log.Info("started", "checkin", fmt.Sprintf("%02d:%02d %s", cfg.CheckinHour, cfg.CheckinMinute, cfg.Loc),
				"grace", cfg.Grace, "missed", cfg.Missed, "contacts", len(contacts))
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				engine.Tick(time.Now())
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		})
	}
	if cfg.Heartbeat != "" {
		run(func() { heartbeat(ctx, cfg.Heartbeat, log) })
	}

	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	wg.Wait()
}

func secretIfEmail(cfg *Config) string {
	if cfg.BaseURL == "" {
		return ""
	}
	return cfg.Secret
}

// heartbeat pings an external dead-man's-switch (e.g. healthchecks.io) so you
// find out if Amialive itself dies, which would otherwise be a silent failure.
func heartbeat(ctx context.Context, url string, log *slog.Logger) {
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if resp, err := httpClient.Do(req); err != nil {
			log.Warn("heartbeat failed", "err", err)
		} else {
			resp.Body.Close()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
