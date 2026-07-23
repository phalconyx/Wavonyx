package registry

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB opens a temp-file SQLite database via the pure-Go modernc driver.
// This also proves the registry works without CGO.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.Join(t.TempDir(), "reg.db") +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRegistryCRUD(t *testing.T) {
	ctx := context.Background()
	r := New(openTestDB(t))
	if err := r.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}

	if err := r.Create(ctx, Session{ID: "personal", WebhookURL: "https://hook"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := r.Create(ctx, Session{ID: "personal"}); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate create: got %v want ErrExists", err)
	}

	got, err := r.Get(ctx, "personal")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != "personal" || got.WebhookURL != "https://hook" || got.JID != "" {
		t.Fatalf("unexpected row: %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("created_at not populated")
	}

	if err := r.UpdateJID(ctx, "personal", "628@s.whatsapp.net"); err != nil {
		t.Fatalf("update jid: %v", err)
	}
	if got, _ = r.Get(ctx, "personal"); got.JID != "628@s.whatsapp.net" {
		t.Fatalf("jid not updated: %q", got.JID)
	}

	if err := r.Create(ctx, Session{ID: "work"}); err != nil {
		t.Fatalf("create work: %v", err)
	}
	list, err := r.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len=%d want 2", len(list))
	}

	if err := r.Delete(ctx, "personal"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r.Get(ctx, "personal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete: got %v want ErrNotFound", err)
	}

	// Operations on missing rows report ErrNotFound.
	if err := r.Delete(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing: got %v", err)
	}
	if err := r.UpdateJID(ctx, "missing", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: got %v", err)
	}
}
