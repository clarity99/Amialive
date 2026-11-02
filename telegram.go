package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Commands is what the bot can ask the engine to do.
type Commands interface {
	CheckIn(via string) string
	Status() string
	Pause(days int) string
	Resume() string
}

// Bot is a Telegram bot using long polling (outbound only; no public URL needed).
type Bot struct {
	token  string
	chatID int64
	hc     *http.Client
	cmds   Commands
	log    *slog.Logger
}

func NewBot(token string, chatID int64, cmds Commands, log *slog.Logger) *Bot {
	return &Bot{token: token, chatID: chatID, cmds: cmds, log: log, hc: &http.Client{Timeout: 45 * time.Second}}
}

type update struct {
	ID      int64 `json:"update_id"`
	Message *struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
	Callback *struct {
		ID      string `json:"id"`
		Data    string `json:"data"`
		Message *struct {
			Chat struct {
				ID int64 `json:"id"`
			} `json:"chat"`
		} `json:"message"`
	} `json:"callback_query"`
}

func (b *Bot) call(ctx context.Context, method string, payload, out any) error {
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+b.token+"/"+method, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.hc.Do(req)
	if err != nil {
		// net/http errors embed the URL, which contains the token.
		return fmt.Errorf("telegram %s: %s", method, strings.ReplaceAll(err.Error(), b.token, "***"))
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool            `json:"ok"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: %s: %w", method, resp.Status, err)
	}
	if !r.OK {
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

func (b *Bot) send(chat int64, text string, markup any) error {
	p := map[string]any{"chat_id": chat, "text": text}
	if markup != nil {
		p["reply_markup"] = markup
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return b.call(ctx, "sendMessage", p, nil)
}

func (b *Bot) Ask(text string) error {
	kb := map[string]any{"inline_keyboard": [][]map[string]string{{{"text": "✅ I'm OK", "callback_data": "ok"}}}}
	return b.send(b.chatID, text, kb)
}

func (b *Bot) Tell(text string) error { return b.send(b.chatID, text, nil) }

// Run polls for updates until ctx is cancelled.
func (b *Bot) Run(ctx context.Context) {
	var offset int64
	for ctx.Err() == nil {
		var ups []update
		err := b.call(ctx, "getUpdates", map[string]any{
			"offset": offset, "timeout": 30, "allowed_updates": []string{"message", "callback_query"},
		}, &ups)
		if err != nil {
			if ctx.Err() == nil {
				b.log.Error("poll failed", "err", err)
				select {
				case <-ctx.Done():
				case <-time.After(5 * time.Second):
				}
			}
			continue
		}
		for _, u := range ups {
			offset = u.ID + 1
			b.handle(ctx, u)
		}
	}
}

func (b *Bot) handle(ctx context.Context, u update) {
	if cb := u.Callback; cb != nil {
		if cb.Message == nil || cb.Message.Chat.ID != b.chatID || b.chatID == 0 {
			return
		}
		reply := "Unknown action"
		if cb.Data == "ok" {
			reply = b.cmds.CheckIn("telegram-button")
		}
		_ = b.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": "Thanks!"}, nil)
		_ = b.send(b.chatID, reply, nil)
		return
	}
	m := u.Message
	if m == nil {
		return
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return
	}
	cmd := strings.ToLower(strings.SplitN(fields[0], "@", 2)[0])

	if b.chatID == 0 || m.Chat.ID != b.chatID {
		// Setup helper: tell an unconfigured bot's first user their chat ID.
		if cmd == "/start" && b.chatID == 0 {
			_ = b.send(m.Chat.ID, fmt.Sprintf("Your chat ID is %d.\nSet TELEGRAM_CHAT_ID=%d and restart Amialive.", m.Chat.ID, m.Chat.ID), nil)
		} else {
			b.log.Warn("ignored message from unauthorized chat", "chat", m.Chat.ID)
		}
		return
	}

	var reply string
	switch cmd {
	case "/ok", "/start":
		reply = b.cmds.CheckIn("telegram-command")
	case "/status":
		reply = b.cmds.Status()
	case "/pause":
		days := 1
		if len(fields) > 1 {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1 || n > 90 {
				_ = b.send(b.chatID, "Usage: /pause <days 1-90>", nil)
				return
			}
			days = n
		}
		reply = b.cmds.Pause(days)
	case "/resume":
		reply = b.cmds.Resume()
	default:
		reply = "Commands:\n/ok - check in\n/status\n/pause <days>\n/resume"
	}
	_ = b.send(b.chatID, reply, nil)
}
