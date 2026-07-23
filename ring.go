package wavonyx

import "sync"

// Ring is a fixed-capacity, concurrency-safe buffer of the most recent inbound
// messages for one session. When full, appending overwrites the oldest entry.
// It backs GET /sessions/{id}/messages as an ephemeral debug/fallback view;
// nothing here is persisted to disk.
type Ring struct {
	mu   sync.Mutex
	buf  []InboundMessage
	next int // index the next Append will write to
	n    int // number of valid entries (0..len(buf))
}

// NewRing returns a Ring holding up to capacity messages. A capacity <= 0 is
// treated as 1 so the buffer is always usable.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 1
	}
	return &Ring{buf: make([]InboundMessage, capacity)}
}

// Cap returns the buffer capacity.
func (r *Ring) Cap() int { return len(r.buf) }

// Len returns the number of buffered messages.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Append adds m, overwriting the oldest entry when the buffer is full.
func (r *Ring) Append(m InboundMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.next] = m
	r.next = (r.next + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// Recent returns up to limit buffered messages, newest first. A limit <= 0 or
// greater than the number of buffered messages returns all of them. The result
// is a fresh copy the caller may retain.
func (r *Ring) Recent(limit int) []InboundMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if limit <= 0 || limit > r.n {
		limit = r.n
	}
	out := make([]InboundMessage, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (r.next - 1 - i + len(r.buf)) % len(r.buf)
		out = append(out, r.buf[idx])
	}
	return out
}
