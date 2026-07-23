# Wavonyx

A lightweight Go backend that wraps WhatsApp so your other applications can send
and receive WhatsApp messages over a small REST API. It is built on
[whatsmeow](https://github.com/tulir/whatsmeow) (a native Go implementation of
the WhatsApp Web multidevice protocol — no browser, no Chromium), supports many
accounts per instance, and pages outgoing messages behind a per‑session queue
with simulated typing to look less like a bot.

> [!WARNING]
> Wavonyx talks to WhatsApp through an **unofficial** protocol. Accounts can be
> banned if WhatsApp decides your traffic looks automated — simulated typing and
> rate limiting reduce the risk but do not remove it. **Use a number you can
> afford to lose**, warm it up gradually, and don't blast bulk messages. For a
> ban‑free, supported path, use the official WhatsApp Cloud API instead (paid,
> business verification, template messages — a very different architecture).

## Features

- **Multi‑session** — one instance manages many WhatsApp numbers, addressed as `/sessions/{id}`.
- **QR login** — the pairing code string is returned in the API response for your client to render; a rotating‑code endpoint keeps pairing alive.
- **Send text** with **simulated typing** — `off`, fixed `constant`, or humanlike `natural` delay, configurable globally and per request; sends are serialized per session with a minimum gap.
- **Receive** — inbound messages are parsed (sender, chat, group/DM, push name, text/caption, quoted) and delivered via **HMAC‑signed webhooks** with retries, plus kept in an in‑memory **ring buffer** for debugging.
- **Lightweight & static** — pure Go, `CGO_ENABLED=0`, runs on a distroless image; WhatsApp credentials persist in a local SQLite file (pure‑Go [modernc](https://pkg.go.dev/modernc.org/sqlite) driver).

## How it works

```
cmd/wavonyx (serve)  ->  server/ (HTTP)  ->  Manager (sessions)  ->  whatsmeow
                              envelope/auth        |                    (WebSocket + E2E)
                                                   +-> SQLite (creds + session registry)
                                                   +-> webhook dispatcher (inbound)
```

Only WhatsApp credentials and a tiny session registry are persisted. Inbound
messages are ephemeral (webhook + ring buffer), never written to disk.

## Quickstart

### From source

```sh
cp .env.example .env          # optional; edit as needed
make run                      # builds and runs `wavonyx serve` on :9900
```

### Docker

```sh
docker compose up -d          # persists credentials in the wavonyx-data volume
```

Health check:

```sh
curl -s localhost:9900/health
# {"data":{"status":"ok","time":"..."},"meta":{"request_id":"req_..."}}
```

If `WAVONYX_API_KEY` is set, send it as `X-API-Key` on every request except `/health`.

## Pair a number and send a message

```sh
KEY="your-api-key"                       # omit the header if auth is disabled
H=(-H "X-API-Key: $KEY")

# 1. create a session
curl -s "${H[@]}" -X POST localhost:9900/sessions -d '{"id":"personal"}'

# 2. start pairing — returns the first QR code string
curl -s "${H[@]}" -X POST localhost:9900/sessions/personal/login
#   -> {"data":{"status":"pairing","qr":{"code":"2@...","expires_in":18}}, ...}

# render the code and scan it in WhatsApp > Linked Devices > Link a Device:
qrencode -t ansiutf8 '2@...'             # brew install qrencode

# the code rotates every ~20s; fetch the latest while pairing:
curl -s "${H[@]}" localhost:9900/sessions/personal/qr

# 3. once linked, status becomes "connected"
curl -s "${H[@]}" localhost:9900/sessions/personal

# 4. send a text (with a per-request typing override)
curl -s "${H[@]}" -X POST localhost:9900/sessions/personal/messages \
  -d '{"to":"6281234567890","text":"hello!","typing":{"mode":"natural"}}'
#   -> {"data":{"message_id":"3EB0...","typing_ms":4211,"queued_ms":3}, ...}
```

`to` accepts an international number (with or without `+` and separators) or an
explicit JID (`...@s.whatsapp.net`, `...@g.us`). Local formats with a leading `0`
are rejected — include the country code.

## Receiving messages

Set `WAVONYX_WEBHOOK_URL` (and optionally a per‑session `webhook_url` on create).
Each inbound message is POSTed as:

```json
{
  "event": "message",
  "session_id": "personal",
  "ts": "2026-07-23T10:00:00Z",
  "data": {
    "message_id": "3EB0...",
    "chat": "6289999@s.whatsapp.net",
    "sender": "6289999@s.whatsapp.net",
    "sender_phone": "6289999",
    "push_name": "Phalconyx",
    "is_group": false,
    "is_from_me": false,
    "timestamp": "2026-07-23T10:00:00Z",
    "kind": "text",
    "text": "hey there",
    "quoted": null
  }
}
```

Headers: `X-Wavonyx-Event`, `X-Wavonyx-Session`, `X-Wavonyx-Delivery`, and — when
`WAVONYX_WEBHOOK_SECRET` is set — `X-Wavonyx-Signature: sha256=<hex>`.

**Verify the signature** over the raw request body:

```sh
# BODY is the exact bytes received; SECRET is WAVONYX_WEBHOOK_SECRET
expected="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}')"
[ "$expected" = "$SIGNATURE_HEADER" ] && echo ok || echo TAMPERED
```

When Wavonyx replies to a chat it first marks that chat's messages as read
(blue ticks), mirroring the human "open, read, type, reply" flow — this is
automatic. For it to be visible, the account's **Read Receipts** privacy setting
(WhatsApp > Settings > Privacy) must be on.

Delivery retries transport errors, `5xx`, and `429` with exponential backoff
(±50% jitter); other `4xx` are treated as permanent. The last messages are also
available without a webhook:

```sh
curl -s "${H[@]}" "localhost:9900/sessions/personal/messages?limit=20"
```

## API reference

| Method & path | Purpose |
|---|---|
| `GET /health` | Liveness (no auth). |
| `POST /sessions` | Create a session. Body: `{"id"?, "webhook_url"?}` (auto id if omitted). |
| `GET /sessions` | List sessions. |
| `GET /sessions/{id}` | Session status (`created`\|`pairing`\|`connected`\|`disconnected`\|`logged_out`). |
| `POST /sessions/{id}/login` | Start pairing; returns the first QR code. |
| `GET /sessions/{id}/qr` | Latest rotating QR code. |
| `POST /sessions/{id}/logout` | Unlink the device (keeps the session row). |
| `DELETE /sessions/{id}` | Delete the session and its device entirely. |
| `POST /sessions/{id}/messages` | Send text. Body: `{"to", "text", "typing"?}`. |
| `GET /sessions/{id}/messages?limit=50` | Recent inbound messages, newest first. |

Every response uses the envelope `{"data": ...}` or
`{"error": {"code", "message"}}` with `{"meta": {"request_id"}}`; the HTTP status
is authoritative. Stable error codes include `unauthorized`, `invalid_json`,
`invalid_session_id`, `missing_text`, `invalid_recipient`, `invalid_typing`,
`invalid_limit`, `session_not_found`, `qr_not_available`, `session_exists`,
`already_connected`, `not_connected`, `not_logged_in`, `queue_full`,
`send_failed`, `unknown_route`, `internal`.

## Configuration

All configuration is via `WAVONYX_*` environment variables — see
[`.env.example`](.env.example) for the full list with defaults. Highlights:

| Variable | Default | |
|---|---|---|
| `WAVONYX_ADDR` | `:9900` | Listen address. |
| `WAVONYX_API_KEY` | *(empty)* | Require `X-API-Key`; empty disables auth. |
| `WAVONYX_DATA_DIR` | `./data` | SQLite location (persist this!). |
| `WAVONYX_DEVICE_NAME` | `Wavonyx` | Name shown in WhatsApp's Linked Devices (set at pairing). |
| `WAVONYX_WEBHOOK_URL` / `_SECRET` | *(empty)* | Inbound delivery + HMAC key. |
| `WAVONYX_TYPING_MODE` | `natural` | `off` \| `constant` \| `natural`. |
| `WAVONYX_SEND_MIN_GAP` | `1s` | Pause between sends per session. |

### Simulated typing

- **`off`** — send immediately.
- **`constant`** — `per_char_ms` × character count.
- **`natural`** — a randomized typing speed per message, per‑word jitter, a short
  initial read delay, and occasional pauses after sentence endings.

Both non‑off modes are clamped to `[min_total, max_total]` (default `400ms`–`15s`,
the ceiling being a hard stall guard). Override per request:

```json
{"to":"628...","text":"...","typing":{"mode":"natural","min_cps":5,"max_cps":8,"max_total_ms":8000}}
```

## Library usage

Wavonyx is library‑first; the HTTP server is a thin layer over the `wavonyx`
package. See [`examples/basic`](examples/basic/main.go):

```sh
go run ./examples/basic 6281234567890
```

## Project layout

```
wavonyx/            core library: manager, session, typing, webhook, wa adapter, parse
  typing/           pure typing-delay calculator
  server/           HTTP handlers (envelope, auth, error mapping)
  internal/registry SQLite session registry
  cmd/wavonyx/      CLI: serve | healthcheck | version | help
  examples/basic/   library usage example
```

## Roadmap

- Media send (image/document/audio) and inbound media download.
- Phone‑number pairing‑code login (QR alternative).
- `session.status` webhook events; read receipts; group tooling.
- Optional persistent message store behind the same ring API.

## License

MIT — see [LICENSE](LICENSE).
