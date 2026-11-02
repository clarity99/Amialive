package main

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Mailer sends plain-text email over SMTP (STARTTLS on 587, implicit TLS on 465).
type Mailer struct{ host, port, user, pass, from string }

func NewMailer(c *Config) *Mailer {
	if c.SMTPHost == "" || c.SMTPFrom == "" {
		return nil
	}
	return &Mailer{c.SMTPHost, c.SMTPPort, c.SMTPUser, c.SMTPPassword, c.SMTPFrom}
}

func clean(s string) string { return strings.NewReplacer("\r", "", "\n", "").Replace(s) }

func (m *Mailer) Send(to, subject, body string) error {
	addr := net.JoinHostPort(m.host, m.port)
	d := net.Dialer{Timeout: 15 * time.Second}
	var conn net.Conn
	var err error
	if m.port == "465" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: m.host})
	} else {
		conn, err = d.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(45 * time.Second))

	c, err := smtp.NewClient(conn, m.host)
	if err != nil {
		return err
	}
	defer c.Close()
	if m.port != "465" {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: m.host}); err != nil {
				return err
			}
		}
	}
	if m.user != "" {
		if err := c.Auth(smtp.PlainAuth("", m.user, m.pass, m.host)); err != nil {
			return err
		}
	}
	from, to := clean(m.from), clean(to)
	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n",
		from, to, mime.QEncoding.Encode("utf-8", clean(subject)), time.Now().Format(time.RFC1123Z))
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		msg += enc[:76] + "\r\n"
		enc = enc[76:]
	}
	msg += enc + "\r\n"
	if _, err := io.WriteString(w, msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

const bulkGateURL = "https://portal.bulkgate.com/api/1.0/simple/transactional"

// BulkGate sends SMS through the BulkGate simple transactional API.
type BulkGate struct{ id, token, sender, senderValue, endpoint string }

func NewBulkGate(c *Config) *BulkGate {
	if c.BulkGateID == "" || c.BulkGateToken == "" {
		return nil
	}
	return &BulkGate{c.BulkGateID, c.BulkGateToken, c.BulkGateSender, c.BulkGateSenderValue, bulkGateURL}
}

func (g *BulkGate) Send(to, body string) error {
	unicode := false
	for _, r := range body {
		if r > 127 {
			unicode = true
		}
	}
	p := map[string]any{
		"application_id":    g.id,
		"application_token": g.token,
		"number":            strings.TrimPrefix(to, "+"), // international format, no plus
		"text":              body,
		"unicode":           unicode,
		"sender_id":         g.sender,
		"tag":               "amialive",
	}
	if g.senderValue != "" {
		p["sender_id_value"] = g.senderValue
	}
	b, _ := json.Marshal(p)
	resp, err := httpClient.Post(g.endpoint, "application/json", bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("bulkgate: %w", err)
	}
	defer resp.Body.Close()
	var r struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = json.Unmarshal(raw, &r)
	if resp.StatusCode/100 != 2 || r.Error != "" || r.Data.Status != "accepted" {
		return fmt.Errorf("bulkgate: %s: %s (%s)", resp.Status, r.Error, r.Type)
	}
	return nil
}

// Alerter fans a message out to a contact's email and phone.
type Alerter struct {
	Mail *Mailer
	SMS  *BulkGate
	Log  *slog.Logger
}

func (a *Alerter) Alert(c Contact, m Message) error {
	var errs []error
	ok := false
	if c.Email != "" {
		if a.Mail == nil {
			errs = append(errs, errors.New("email not configured"))
		} else if err := a.Mail.Send(c.Email, m.Subject, m.Body); err != nil {
			errs = append(errs, fmt.Errorf("email: %w", err))
		} else {
			ok = true
		}
	}
	if c.Phone != "" {
		if a.SMS == nil {
			errs = append(errs, errors.New("sms not configured"))
		} else if err := a.SMS.Send(c.Phone, m.SMS); err != nil {
			errs = append(errs, fmt.Errorf("sms: %w", err))
		} else {
			ok = true
		}
	}
	if ok {
		for _, err := range errs {
			a.Log.Warn("partial delivery", "contact", c.Name, "err", err)
		}
		return nil
	}
	return errors.Join(errs...)
}

// EmailUser prompts the monitored user by email with a one-click check-in link.
type EmailUser struct {
	Mail *Mailer
	To   string
	Link string
}

func (u *EmailUser) Ask(text string) error {
	return u.Mail.Send(u.To, "Amialive: are you OK?", text+"\n\nCheck in here:\n"+u.Link+"\n")
}

func (u *EmailUser) Tell(text string) error { return u.Mail.Send(u.To, "Amialive", text+"\n") }
