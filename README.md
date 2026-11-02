# Amialive

A tiny "am I alive?" dead-man's switch. Every day it asks you to confirm you're OK.
If you miss a check-in (configurable), it emails and texts your emergency contacts.

Go, standard library only, one static binary, runs under Docker Compose on arm64 (or amd64).

## How it works

1. At `CHECKIN_TIME` it sends you a prompt with an **I'm OK** button (Telegram).
2. Until you answer it re-pings every `REMINDER_INTERVAL`, and tells you exactly when contacts will be alerted.
3. If you haven't answered `GRACE_PERIOD` after the `MISSED_CHECKINS`-th unanswered prompt, every contact is alerted by email (SMTP) and SMS (BulkGate). Failed deliveries are retried every minute.
4. When you check in afterwards, contacts get an **all-clear**.

Safety details: state survives restarts; if Amialive itself was offline, the grace period restarts instead of raising a false alarm; check-ins from anyone but your Telegram chat are ignored.

Telegram commands: `/ok`, `/status`, `/pause <days>` (holiday mode), `/resume`.

## Choosing the check-in channel

| Channel | Verdict |
|---|---|
| **Telegram bot** (default) | Free, reliable push, one-tap button, and it needs only *outbound* connections (long polling): no public URL, TLS or port-forward on your home arm64 box. |
| Email magic link (optional fallback) | Independent of any messenger, so it covers a Telegram outage. Needs a public HTTPS URL. The link opens a confirm page; the check-in is a POST so mail scanners can't trigger it. |
| Slack | Works via Socket Mode, but workspace-bound and easy to mute. Better for work than a personal safety net. |
| PWA push | Most work for the least reliability: public HTTPS, VAPID keys, service worker, and iOS only pushes to installed PWAs. |

Enable both Telegram and the email link if you want redundancy.

## Setup

```sh
cp .env.example .env                       # fill in
cp contacts.example.json contacts.json     # your emergency contacts (phones in E.164)
```

1. Create a Telegram bot with [@BotFather](https://t.me/BotFather), put the token in `TELEGRAM_BOT_TOKEN`.
2. `docker compose up -d`, message your bot `/start`. It replies with your chat ID: set `TELEGRAM_CHAT_ID` and `docker compose up -d` again.
3. Verify contact delivery: `docker compose run --rm amialive -test-contacts`

Images are published to `ghcr.io/clarity99/amialive` (multi-arch arm64 + amd64) by GitHub Actions. If the package is private, `docker login ghcr.io` first, or make it public in the package settings.

### Configuration

See `.env.example`. Key knobs: `CHECKIN_TIME`, `TZ`, `GRACE_PERIOD`, `MISSED_CHECKINS`, `REMINDER_INTERVAL`.
The example: `MISSED_CHECKINS=2`, `GRACE_PERIOD=4h` alerts only after two consecutive unanswered days, 4h after the second prompt.

### Email check-in link (optional)

Set `USER_EMAIL`, `BASE_URL` (e.g. `https://alive.example.com`) and `CHECKIN_SECRET`, publish port 8080 behind a TLS reverse proxy.

## Who watches the watcher?

If the host or container dies, nobody is alerted. Set `HEARTBEAT_URL` to a service such as healthchecks.io so *it* tells you when Amialive stops pinging. Also keep a restart policy (already `unless-stopped`) and test with `-test-contacts` occasionally.

## CI

- `ci.yml`: gofmt, vet, `go test -race` on every push/PR.
- `docker.yml`: builds linux/arm64 + amd64 (cross-compiled, no QEMU) and pushes to GHCR on pushes to the default branch and `v*` tags. PRs build without pushing.

## Development

```sh
go test -race ./...
```
