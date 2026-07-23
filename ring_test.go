package wavonyx

import (
	"fmt"
	"sync"
	"testing"
)

func ids(ms []InboundMessage) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.MessageID
	}
	return out
}

func equalIDs(ms []InboundMessage, want []string) bool {
	if len(ms) != len(want) {
		return false
	}
	for i := range ms {
		if ms[i].MessageID != want[i] {
			return false
		}
	}
	return true
}

func appendIDs(r *Ring, ids ...string) {
	for _, id := range ids {
		r.Append(InboundMessage{MessageID: id})
	}
}

func TestRingEmpty(t *testing.T) {
	r := NewRing(4)
	if r.Len() != 0 {
		t.Fatalf("len=%d want 0", r.Len())
	}
	if got := r.Recent(0); len(got) != 0 {
		t.Fatalf("recent on empty: got %d entries", len(got))
	}
	if got := r.Recent(5); len(got) != 0 {
		t.Fatalf("recent(limit) on empty: got %d entries", len(got))
	}
}

func TestRingRecentNewestFirst(t *testing.T) {
	r := NewRing(3)
	appendIDs(r, "a", "b")
	got := r.Recent(0)
	if !equalIDs(got, []string{"b", "a"}) {
		t.Fatalf("newest-first: got %v", ids(got))
	}
}

func TestRingWraparound(t *testing.T) {
	r := NewRing(3)
	appendIDs(r, "a", "b", "c", "d", "e")
	if got := r.Recent(0); !equalIDs(got, []string{"e", "d", "c"}) {
		t.Fatalf("wraparound: got %v want [e d c]", ids(got))
	}
	if r.Len() != 3 || r.Cap() != 3 {
		t.Fatalf("len=%d cap=%d want 3/3", r.Len(), r.Cap())
	}
}

func TestRingRecentLimit(t *testing.T) {
	r := NewRing(5)
	appendIDs(r, "a", "b", "c", "d")
	if got := r.Recent(2); !equalIDs(got, []string{"d", "c"}) {
		t.Fatalf("limit 2: got %v", ids(got))
	}
	if got := r.Recent(100); len(got) != 4 {
		t.Fatalf("limit > len: got %d want 4", len(got))
	}
	if got := r.Recent(-1); len(got) != 4 {
		t.Fatalf("negative limit should return all: got %d", len(got))
	}
}

func TestNewRingMinCapacity(t *testing.T) {
	r := NewRing(0)
	if r.Cap() != 1 {
		t.Fatalf("cap=%d want 1", r.Cap())
	}
	appendIDs(r, "a", "b")
	if got := r.Recent(0); len(got) != 1 || got[0].MessageID != "b" {
		t.Fatalf("min-capacity ring: got %v", ids(got))
	}
}

func TestRingConcurrent(t *testing.T) {
	r := NewRing(64)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				r.Append(InboundMessage{MessageID: fmt.Sprintf("%d-%d", g, i)})
				if i%16 == 0 {
					_ = r.Recent(10)
				}
			}
		}(g)
	}
	wg.Wait()
	if r.Len() != r.Cap() {
		t.Fatalf("after many appends, len=%d want cap=%d", r.Len(), r.Cap())
	}
}
