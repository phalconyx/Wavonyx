package wavonyx

import (
	"testing"
	"time"

	"github.com/phalconyx/wavonyx/typing"
)

func TestDefaultConfig(t *testing.T) {
	c := DefaultConfig()
	if c.DataDir != "./data" {
		t.Fatalf("DataDir=%q", c.DataDir)
	}
	if c.SendQueue != 100 {
		t.Fatalf("SendQueue=%d want 100", c.SendQueue)
	}
	if c.RingSize != 200 {
		t.Fatalf("RingSize=%d want 200", c.RingSize)
	}
	if c.SendMinGap != time.Second {
		t.Fatalf("SendMinGap=%v want 1s", c.SendMinGap)
	}
	if c.Typing.Mode != typing.ModeNatural {
		t.Fatalf("Typing.Mode=%v want natural", c.Typing.Mode)
	}
}

func TestConfigWithDefaults(t *testing.T) {
	c := Config{}.withDefaults()
	if c.DataDir == "" || c.SendQueue <= 0 || c.RingSize <= 0 {
		t.Fatalf("structural defaults not filled: %+v", c)
	}
	if c.Typing.Mode == "" {
		t.Fatal("typing mode not defaulted")
	}
	// Negative SendMinGap is clamped to zero.
	if got := (Config{SendMinGap: -5}).withDefaults(); got.SendMinGap != 0 {
		t.Fatalf("negative SendMinGap not clamped: %v", got.SendMinGap)
	}
	// Explicit values survive.
	custom := Config{DataDir: "/tmp/x", SendQueue: 7, RingSize: 9, SendMinGap: 2 * time.Second}
	got := custom.withDefaults()
	if got.DataDir != "/tmp/x" || got.SendQueue != 7 || got.RingSize != 9 || got.SendMinGap != 2*time.Second {
		t.Fatalf("explicit values overwritten: %+v", got)
	}
}
