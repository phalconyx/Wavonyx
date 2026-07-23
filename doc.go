// Package wavonyx is a lightweight, multi-session WhatsApp gateway. It wraps the
// whatsmeow multidevice protocol library so other services can send and receive
// WhatsApp messages over a small HTTP API, using WhatsApp as a notification and
// messaging channel.
//
// The code is organised library-first, mirroring the sibling projects telconyx
// and dragonyx:
//
//   - the root package (this one) is the reusable core: the session manager,
//     message types, the inbound ring buffer, the webhook dispatcher, and the
//     whatsmeow adapter — the latter kept behind a narrow interface so it can be
//     replaced with an in-memory fake in tests;
//   - package [github.com/phalconyx/wavonyx/typing] computes human-like typing
//     delays used to pace outgoing messages, reducing the risk of being flagged
//     as a bot;
//   - package github.com/phalconyx/wavonyx/server exposes the HTTP API;
//   - command wavonyx (cmd/wavonyx) wires configuration from WAVONYX_* env vars.
//
// WhatsApp credentials are persisted to a local SQLite database (via whatsmeow's
// sqlstore) so paired sessions survive restarts without re-scanning a QR code.
// Inbound messages are deliberately ephemeral: they are delivered to a webhook
// and retained only in a per-session in-memory ring buffer, never written to
// disk.
//
// This is a work in progress; packages are being added milestone by milestone.
package wavonyx
