// Package registry stores the mapping from Wavonyx session ids to whatsmeow
// device JIDs (plus an optional per-session webhook override). It lives in the
// same SQLite database as the whatsmeow session store, in its own
// wavonyx_sessions table, and speaks plain database/sql so it is driver-neutral
// (the service wires it to the pure-Go modernc.org/sqlite driver).
package registry

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Sentinel errors.
var (
	ErrNotFound = errors.New("registry: session not found")
	ErrExists   = errors.New("registry: session already exists")
)

// Session is one row of the registry.
type Session struct {
	ID         string
	JID        string // whatsmeow device JID; "" until paired
	WebhookURL string // per-session override; "" means use the global default
	CreatedAt  time.Time
}

// Registry is the session-mapping table on a shared *sql.DB.
type Registry struct {
	db *sql.DB
}

// New returns a Registry backed by db. Call Init once before use.
func New(db *sql.DB) *Registry { return &Registry{db: db} }

const schema = `
CREATE TABLE IF NOT EXISTS wavonyx_sessions (
	id          TEXT PRIMARY KEY,
	jid         TEXT NOT NULL DEFAULT '',
	webhook_url TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL DEFAULT 0
);`

// Init creates the table if it does not already exist.
func (r *Registry) Init(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, schema)
	return err
}

// Create inserts a new session row, returning ErrExists if the id is taken.
func (r *Registry) Create(ctx context.Context, s Session) error {
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now()
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO wavonyx_sessions (id, jid, webhook_url, created_at) VALUES (?, ?, ?, ?)`,
		s.ID, s.JID, s.WebhookURL, s.CreatedAt.UTC().Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return ErrExists
		}
		return err
	}
	return nil
}

// Get returns the session with the given id, or ErrNotFound.
func (r *Registry) Get(ctx context.Context, id string) (Session, error) {
	var (
		s           Session
		createdUnix int64
	)
	err := r.db.QueryRowContext(ctx,
		`SELECT id, jid, webhook_url, created_at FROM wavonyx_sessions WHERE id = ?`, id).
		Scan(&s.ID, &s.JID, &s.WebhookURL, &createdUnix)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	s.CreatedAt = time.Unix(createdUnix, 0).UTC()
	return s, nil
}

// List returns all sessions ordered by creation time then id.
func (r *Registry) List(ctx context.Context) ([]Session, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, jid, webhook_url, created_at FROM wavonyx_sessions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var (
			s           Session
			createdUnix int64
		)
		if err := rows.Scan(&s.ID, &s.JID, &s.WebhookURL, &createdUnix); err != nil {
			return nil, err
		}
		s.CreatedAt = time.Unix(createdUnix, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateJID sets the device JID for a session.
func (r *Registry) UpdateJID(ctx context.Context, id, jid string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE wavonyx_sessions SET jid = ? WHERE id = ?`, jid, id)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// UpdateWebhook sets the per-session webhook URL override.
func (r *Registry) UpdateWebhook(ctx context.Context, id, url string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE wavonyx_sessions SET webhook_url = ? WHERE id = ?`, url, id)
	if err != nil {
		return err
	}
	return requireOne(res)
}

// Delete removes a session row, returning ErrNotFound if it did not exist.
func (r *Registry) Delete(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM wavonyx_sessions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireOne(res)
}

func requireOne(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// isUniqueViolation reports whether err is a SQLite UNIQUE/primary-key
// constraint failure. Matched by message to stay driver-neutral.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
