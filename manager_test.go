package wavonyx

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/phalconyx/wavonyx/internal/registry"
	"go.mau.fi/whatsmeow/store"
)

// fakeSink is a webhookSink that records enqueued events for assertions.
type fakeSink struct {
	mu     sync.Mutex
	events []Event
}

func (f *fakeSink) Enqueue(url string, ev Event) {
	f.mu.Lock()
	f.events = append(f.events, ev)
	f.mu.Unlock()
}
func (f *fakeSink) Close() {}
func (f *fakeSink) all() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Event, len(f.events))
	copy(out, f.events)
	return out
}

// newTestManagerSink is newTestManager with a recording webhook sink.
func newTestManagerSink(t *testing.T, store *fakeStore, cfg Config) (*Manager, *fakeSink) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "mgr.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(db)
	if err := reg.Init(context.Background()); err != nil {
		t.Fatalf("registry init: %v", err)
	}
	sink := &fakeSink{}
	m := newManager(cfg.withDefaults(), store, reg, sink, nil, nil)
	t.Cleanup(func() { _ = m.Close() })
	return m, sink
}

// newTestManager builds a Manager backed by an in-memory fake store, a real
// registry on a temp modernc.org/sqlite database, and a no-URL dispatcher
// (deliveries are no-ops). Everything is hermetic.
func newTestManager(t *testing.T, store *fakeStore, cfg Config) (*Manager, *registry.Registry) {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "mgr.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	reg := registry.New(db)
	if err := reg.Init(context.Background()); err != nil {
		t.Fatalf("registry init: %v", err)
	}
	hooks := NewDispatcher(WebhookConfig{}, nil)
	m := newManager(cfg.withDefaults(), store, reg, hooks, nil, nil)
	t.Cleanup(func() { _ = m.Close() })
	return m, reg
}

func waitUntil(t *testing.T, cond func() bool, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func waitStatus(t *testing.T, m *Manager, id string, want Status, d time.Duration) {
	t.Helper()
	waitUntil(t, func() bool {
		info, err := m.Get(context.Background(), id)
		return err == nil && info.Status == want
	}, d)
}

func create(t *testing.T, m *Manager, id string) {
	t.Helper()
	if _, err := m.Create(context.Background(), id, ""); err != nil {
		t.Fatalf("create %q: %v", id, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagerCreateGetListDelete(t *testing.T) {
	ctx := context.Background()
	m, reg := newTestManager(t, newFakeStore(), Config{})

	if _, err := m.Create(ctx, "Bad ID!", ""); !errors.Is(err, ErrInvalidSessionID) {
		t.Fatalf("invalid id: %v", err)
	}

	info, err := m.Create(ctx, "personal", "https://hook")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.ID != "personal" || info.Status != StatusCreated || info.WebhookURL != "https://hook" {
		t.Fatalf("info: %+v", info)
	}

	if _, err := m.Create(ctx, "personal", ""); !errors.Is(err, ErrSessionExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if row, err := reg.Get(ctx, "personal"); err != nil || row.WebhookURL != "https://hook" {
		t.Fatalf("registry row: %+v err=%v", row, err)
	}

	auto, err := m.Create(ctx, "", "")
	if err != nil {
		t.Fatalf("auto create: %v", err)
	}
	if auto.ID == "" {
		t.Fatal("empty auto id")
	}

	if _, err := m.Get(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("get missing: %v", err)
	}
	if list := m.List(ctx); len(list) != 2 {
		t.Fatalf("list len=%d want 2", len(list))
	}

	if err := m.Delete(ctx, "personal"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, "personal"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := reg.Get(ctx, "personal"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("registry after delete: %v", err)
	}
	if err := m.Delete(ctx, "missing"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestManagerStartAttaches(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.markPaired("628@s.whatsapp.net")
	m, reg := newTestManager(t, store, Config{})

	must(t, reg.Create(ctx, registry.Session{ID: "paired", JID: "628@s.whatsapp.net"}))
	must(t, reg.Create(ctx, registry.Session{ID: "gone", JID: "629@s.whatsapp.net"}))
	must(t, reg.Create(ctx, registry.Session{ID: "fresh"}))

	if err := m.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}

	waitStatus(t, m, "paired", StatusConnected, 2*time.Second)
	if info, _ := m.Get(ctx, "gone"); info.Status != StatusLoggedOut {
		t.Fatalf("gone status: %v", info.Status)
	}
	if info, _ := m.Get(ctx, "fresh"); info.Status != StatusCreated {
		t.Fatalf("fresh status: %v", info.Status)
	}
}

// TestNewManagerSetsDeviceName exercises the real NewManager path (opening and
// migrating a temp SQLite DB) and checks the device name is applied globally.
func TestNewManagerSetsDeviceName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.DeviceName = "MyBot"
	m, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	defer m.Close()
	if got := store.DeviceProps.GetOs(); got != "MyBot" {
		t.Fatalf("device name = %q want MyBot", got)
	}
}
