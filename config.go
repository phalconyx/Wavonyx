package wavonyx

import (
	"log/slog"
	"time"

	"github.com/phalconyx/wavonyx/typing"
)

// Config configures a Manager. The zero value is usable via withDefaults, which
// fills unset fields (the zero-value-then-default style used across the sibling
// projects); DefaultConfig returns the same defaults explicitly.
type Config struct {
	// DataDir is the directory holding the SQLite session store. Default: "./data".
	DataDir string

	// DeviceName is the name shown in WhatsApp's Linked Devices list. It is only
	// recorded at pairing time, so changing it affects new pairings, not
	// already-linked sessions. Default: "Wavonyx".
	DeviceName string

	// Typing is the default typing simulation for sends that don't override it.
	Typing typing.Config

	// SendMinGap is the minimum pause after each send on a session before the
	// next one starts, smoothing bursts (anti-ban). Zero means no gap.
	SendMinGap time.Duration

	// SendQueue is the per-session outbound queue depth. A send arriving while
	// the queue is full fails fast with ErrQueueFull. Default: 100.
	SendQueue int

	// RingSize is the per-session inbound ring-buffer capacity. Default: 200.
	RingSize int

	// IncludeFromMe controls whether messages authored on the linked phone or
	// other devices (IsFromMe) are delivered to the webhook and ring buffer.
	IncludeFromMe bool

	// Webhook configures inbound-message delivery. A zero Webhook.URL disables
	// delivery; a per-session override can still enable it for that session.
	Webhook WebhookConfig

	// Logger is the structured logger for the manager and sessions. Defaults to
	// slog.Default().
	Logger *slog.Logger
}

// DefaultConfig returns a Config populated with the library defaults.
func DefaultConfig() Config {
	return Config{
		DataDir:       "./data",
		DeviceName:    "Wavonyx",
		Typing:        typing.Default(),
		SendMinGap:    time.Second,
		SendQueue:     100,
		RingSize:      200,
		IncludeFromMe: false,
	}
}

// withDefaults fills unset structural fields. SendMinGap and IncludeFromMe are
// passed through (0 and false are valid, meaningful values).
func (c Config) withDefaults() Config {
	if c.DataDir == "" {
		c.DataDir = "./data"
	}
	if c.DeviceName == "" {
		c.DeviceName = "Wavonyx"
	}
	if c.SendMinGap < 0 {
		c.SendMinGap = 0
	}
	if c.SendQueue <= 0 {
		c.SendQueue = 100
	}
	if c.RingSize <= 0 {
		c.RingSize = 200
	}
	if c.Typing.Mode == "" {
		c.Typing = typing.Default()
	}
	return c
}
