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
- **QR login** — the pairing code is returned by the API for your client to render, and `wavonyx connect` draws a scannable QR right in your terminal.
- **Built‑in CLI** — pair, list, send, and tail messages from the shell, against a local or remote server.
- **Send** text or media (image/video/audio/document) with **simulated typing** — `off`, fixed `constant`, or humanlike `natural` delay, configurable globally and per request; sends are serialized per session with a minimum gap.
- **Receive** — inbound messages (text and media) are parsed (sender, chat, group/DM, push name, caption, quoted) and delivered via **HMAC‑signed webhooks** with retries, plus kept in an in‑memory **ring buffer**; media bytes are fetched on demand with a token.
- **Lightweight & static** — pure Go, `CGO_ENABLED=0`, runs on a distroless image; WhatsApp credentials persist in a local SQLite file (pure‑Go [modernc](https://pkg.go.dev/modernc.org/sqlite) driver).

## How it works

```
  your app  ─┐
             ├─HTTP→  server/  →  Manager (sessions)  →  whatsmeow
  wavonyx   ─┘        envelope        |                  (WebSocket + E2E)
  CLI                 + auth          ├→ SQLite (creds + session registry)
                                      └→ webhook dispatcher (inbound)
```

`wavonyx serve` owns the WhatsApp connections; everything else — your app and
the CLI alike — talks to it over HTTP. Only WhatsApp credentials and a tiny
session registry are persisted; inbound messages are ephemeral (webhook + ring
buffer), never written to disk.

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

## Command line

The same `wavonyx` binary is both the server and its client. `wavonyx serve`
runs the gateway; every other command is a thin HTTP client for a **running**
server.

That split is deliberate: a WhatsApp session is a long‑lived WebSocket
connection, so a one‑shot command can't hold one, and two processes sharing the
same device store would fight over it (WhatsApp drops one with `StreamReplaced`).
The upside is that the CLI works from anywhere that can reach the server.

### Pointing the CLI at a server

| Where the server runs | What to do |
|---|---|
| Same machine, default port | Nothing — the CLI defaults to `http://127.0.0.1:9900`. |
| Docker on this machine | Nothing — compose publishes the port, so the default still works. |
| Another machine | `export WAVONYX_URL=https://wa.example.com` (or pass `--url`). |

### Authentication

When the server runs with `WAVONYX_API_KEY` set, the CLI must present the same
key. Either export it once per shell:

```sh
export WAVONYX_API_KEY=your-key
wavonyx list
```

or pass it per command, which overrides the environment:

```sh
wavonyx --key your-key list
```

Without it you get a `401 unauthorized` and a reminder of both options.

> [!NOTE]
> `.env` is only read by `docker compose`, never by the binary itself. If you
> keep your key there and run the server with `make run`, load it into your
> shell first — `set -a; source .env; set +a` — which configures the server and
> the CLI in one go.

For a remote server, put it behind TLS and never expose port 9900 unprotected.

You can also run it inside the container, though you rarely need to. The image
is distroless, so there's no shell — call the binary by its full path:

```sh
docker compose exec wavonyx /usr/local/bin/wavonyx list
```

### Pairing a phone

```sh
wavonyx connect personal
```

This creates the session if needed, then draws the QR code in your terminal and
keeps it fresh as WhatsApp rotates it:

```
Session "personal" — scan this with WhatsApp on your phone:
  WhatsApp › Settings › Linked Devices › Link a Device

    █▀▀▀▀▀█ ▄▀ ▄█▀▄ █▀▀▀▀▀█
    █ ███ █ ▀█▄▀▄▄█ █ ███ █        (a real, scannable QR code)
    █▄▄▄▄▄█ ▀ ▄▀▄ ▀ █▄▄▄▄▄█
    …

Waiting for the scan… the code refreshes by itself. Press Ctrl-C to cancel.
```

Scan it, and the line is replaced by `✓ Session "personal" connected as … `.
The session then lives on in the server — the command exiting doesn't disconnect
it, and it reconnects by itself after a server restart.

Your terminal needs to be ~70 columns wide for the code to render intact. If
your scanner struggles, try `--invert` (helps on some light themes) or
`--no-color`.

### Commands

**Server**

| Command | What it does |
|---|---|
| `wavonyx serve` | Run the gateway (this is the process everything else talks to) |
| `wavonyx healthcheck` | Probe the local server's `/health`; used as the container healthcheck |

**Sessions**

| Command | What it does |
|---|---|
| `wavonyx connect [id]` | Pair an account, showing a live QR code |
| `wavonyx list` | List sessions and their status |
| `wavonyx status [id]` | Show one session in detail |
| `wavonyx logout [id]` | Unlink the device, keep the session |
| `wavonyx delete <id>` | Delete the session and its credentials |

**Messages**

| Command | What it does |
|---|---|
| `wavonyx send <id> <to> <text…>` | Send a text message |
| `wavonyx send --file f.pdf <id> <to> [caption…]` | Send an attachment |
| `wavonyx messages [id] [-n 20]` | Show recent inbound messages |
| `wavonyx watch [id]` | Print inbound messages as they arrive |
| `wavonyx edit <id> <to> <msg-id> <text…>` | Edit a message you sent |
| `wavonyx revoke <id> <to> <msg-id>` | Delete a message you sent, for everyone |
| `wavonyx media <id> <token> [-o file]` | Download an inbound attachment |

**Help**

| Command | What it does |
|---|---|
| `wavonyx help` | Summary of every command |
| `wavonyx help <command>` | Help for one command (same as `wavonyx <command> -h`) |
| `wavonyx version` | Print the version |

The session id defaults to `default` where it's optional. Every client command
accepts `--url`, `--key`, `--json`, and `--timeout`, before or after the command
name; other flags go before the positional arguments.

### Everyday use

```sh
wavonyx list
# ID        STATUS     PHONE          NAME       CREATED
# personal  connected  6281234567890  Phalconyx  2026-07-24 13:04

wavonyx send personal 6281234567890 hello from my terminal
wavonyx send --file invoice.pdf personal 6281234567890 your invoice is ready

wavonyx watch personal
# 13:07:12  Alex (628999…)          hey, got it
# 13:07:40  Alex (628999…)          [image] here's the photo

wavonyx messages personal -n 5
```

For scripting, `--json` prints the raw payload — handy with `jq`:

```sh
wavonyx list --json | jq -r '.[] | select(.status=="connected") | .id'

# download the newest attachment
TOKEN=$(wavonyx messages personal --json | jq -r '[.[] | select(.media)][0].media.token')
wavonyx media personal "$TOKEN" -o attachment
```

## HTTP API walkthrough

The CLI covers the same ground, but here is the raw API if you're integrating
from another language.

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

Send **media** as `multipart/form-data` — the `file` part is the attachment;
`caption`, `typing`, and `to` are form fields:

```sh
curl -s "${H[@]}" -X POST localhost:9900/sessions/personal/messages \
  -F "to=6281234567890" -F "caption=here you go" -F "file=@invoice.pdf"
```

The kind (image/video/audio/document) is detected from the file's MIME type;
files above `WAVONYX_MAX_MEDIA_BYTES` (default 64 MB) are rejected.

**Edit or delete** a message you already sent — use the `message_id` from the
send response and the same `to`:

```sh
# edit the text
curl -s "${H[@]}" -X POST localhost:9900/sessions/personal/messages/<message_id>/edit \
  -d '{"to":"6281234567890","text":"corrected text"}'

# delete for everyone
curl -s "${H[@]}" -X DELETE "localhost:9900/sessions/personal/messages/<message_id>?to=6281234567890"
```

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

**Media messages.** When `kind` is `image`, `video`, `audio`, `voice`,
`document`, or `sticker`, the payload carries a `media` object (mimetype, size,
dimensions/duration, filename) with an opaque `token`. Any caption is in `text`.
Download the bytes on demand:

```sh
curl -s "${H[@]}" "localhost:9900/sessions/personal/media?token=<token>" -o out.jpg
```

The token is self-contained (no server-side state) but tied to WhatsApp's media
CDN, whose paths expire after a while — download reasonably promptly.

**Edits and deletes.** Switch on the webhook `event` field to keep a stored copy
in sync:

- `message` — a new message.
- `message.edit` — a message's content changed. The payload holds the new
  content plus `edited_id`, the id of the message being edited.
- `message.revoke` — a message was deleted for everyone; the payload's
  `message_id` is the deleted message.

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
| `POST /sessions/{id}/messages` | Send text (JSON `{"to","text","typing"?}`) or media (multipart: `to`, `caption`, `typing`, `file`). |
| `GET /sessions/{id}/messages?limit=50` | Recent inbound messages, newest first. |
| `GET /sessions/{id}/media?token=…` | Download an inbound attachment (streams raw bytes). |
| `POST /sessions/{id}/messages/{message_id}/edit` | Edit a message you sent. Body: `{"to","text"}`. |
| `DELETE /sessions/{id}/messages/{message_id}` | Delete a message you sent, for everyone (`?to=` or body `{"to"}`). |

Every response uses the envelope `{"data": ...}` or
`{"error": {"code", "message"}}` with `{"meta": {"request_id"}}`; the HTTP status
is authoritative. Stable error codes include `unauthorized`, `invalid_json`,
`invalid_session_id`, `missing_text`, `invalid_recipient`, `invalid_typing`,
`invalid_limit`, `invalid_token`, `invalid_multipart`, `missing_file`,
`missing_media`, `session_not_found`, `qr_not_available`, `session_exists`,
`already_connected`, `not_connected`, `not_logged_in`, `queue_full`,
`media_too_large`, `send_failed`, `media_download_failed`, `unknown_route`,
`internal`.

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
| `WAVONYX_MAX_MEDIA_BYTES` | `64MB` | Max outbound media file size. |

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
  cmd/wavonyx/      server + CLI (client.go, cmd_session.go, cmd_message.go, qr.go)
  examples/basic/   library usage example
```

## Roadmap

- Phone‑number pairing‑code login (QR alternative).
- `session.status` webhook events; group tooling.
- Optional persistent message store behind the same ring API.
- Streaming inbound events (SSE/WebSocket) so `watch` doesn't poll.

## License

MIT — see [LICENSE](LICENSE).
