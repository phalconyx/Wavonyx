package typing

import (
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// newRNG returns a deterministic generator so natural-mode results are stable.
func newRNG(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))
}

func TestDurationOff(t *testing.T) {
	cfg := Config{Mode: ModeOff}
	if got := Duration("hello world", cfg, newRNG(1)); got != 0 {
		t.Fatalf("off mode: got %v, want 0", got)
	}
}

func TestDurationEmptyText(t *testing.T) {
	for _, m := range []Mode{ModeOff, ModeConstant, ModeNatural} {
		if got := Duration("", Config{Mode: m}, newRNG(1)); got != 0 {
			t.Fatalf("mode %s empty text: got %v, want 0", m, got)
		}
	}
}

func TestDurationConstantExact(t *testing.T) {
	// 100 runes * 50ms = 5s, inside [400ms, 15s] -> returned as-is.
	text := strings.Repeat("a", 100)
	cfg := Config{Mode: ModeConstant, PerChar: 50 * time.Millisecond}
	if got, want := Duration(text, cfg, nil), 5*time.Second; got != want {
		t.Fatalf("constant exact: got %v, want %v", got, want)
	}
}

func TestDurationConstantClampMax(t *testing.T) {
	// 1000 runes * 55ms (default) = 55s -> clamped to the 15s default ceiling.
	text := strings.Repeat("a", 1000)
	if got := Duration(text, Config{Mode: ModeConstant}, nil); got != 15*time.Second {
		t.Fatalf("constant max clamp: got %v, want 15s", got)
	}
}

func TestDurationConstantClampMin(t *testing.T) {
	// "hi" = 2 runes * 55ms = 110ms -> clamped up to the 400ms default floor.
	if got := Duration("hi", Config{Mode: ModeConstant}, nil); got != 400*time.Millisecond {
		t.Fatalf("constant min clamp: got %v, want 400ms", got)
	}
}

func TestDurationRuneCountingNotBytes(t *testing.T) {
	// 5 emoji = 5 runes but 20 bytes; must match 5 ASCII runes exactly.
	cfg := Config{Mode: ModeConstant, PerChar: 100 * time.Millisecond, MaxTotal: time.Minute}
	ascii := Duration("hello", cfg, nil)
	emoji := Duration("\U0001F600\U0001F600\U0001F600\U0001F600\U0001F600", cfg, nil)
	if ascii != emoji {
		t.Fatalf("rune counting: ascii=%v emoji=%v, want equal", ascii, emoji)
	}
	if ascii != 500*time.Millisecond {
		t.Fatalf("rune counting: got %v, want 500ms", ascii)
	}
}

func TestDurationNaturalDeterministic(t *testing.T) {
	cfg := Config{Mode: ModeNatural}
	text := "Halo, apa kabar? Semoga harimu menyenangkan."
	a := Duration(text, cfg, newRNG(42))
	b := Duration(text, cfg, newRNG(42))
	if a != b {
		t.Fatalf("natural not deterministic: %v vs %v", a, b)
	}
	if a <= 0 {
		t.Fatalf("natural duration should be > 0, got %v", a)
	}
}

func TestDurationNaturalWithinBounds(t *testing.T) {
	cfg := Config{Mode: ModeNatural, MinTotal: 400 * time.Millisecond, MaxTotal: 15 * time.Second}
	text := "Halo, apa kabar? Semoga harimu menyenangkan sekali hari ini."
	for seed := uint64(0); seed < 200; seed++ {
		d := Duration(text, cfg, newRNG(seed))
		if d < cfg.MinTotal || d > cfg.MaxTotal {
			t.Fatalf("seed %d: %v out of bounds [%v, %v]", seed, d, cfg.MinTotal, cfg.MaxTotal)
		}
	}
}

func TestDurationNaturalClampMax(t *testing.T) {
	// A very long message must always hit the ceiling regardless of the draw.
	text := strings.Repeat("kata ", 10000)
	cfg := Config{Mode: ModeNatural, MaxTotal: 15 * time.Second}
	for seed := uint64(0); seed < 25; seed++ {
		if d := Duration(text, cfg, newRNG(seed)); d != 15*time.Second {
			t.Fatalf("seed %d: long text got %v, want 15s", seed, d)
		}
	}
}

func TestDurationNaturalNilRNGDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("natural with nil rng panicked: %v", r)
		}
	}()
	if d := Duration("hello world", Config{Mode: ModeNatural}, nil); d <= 0 {
		t.Fatalf("natural nil rng: got %v, want > 0", d)
	}
}

func TestDurationZeroConfigDefaultsToNatural(t *testing.T) {
	// Empty Mode should default to natural and produce a non-zero delay.
	if d := Duration("hello world", Config{}, newRNG(7)); d <= 0 {
		t.Fatalf("zero config: got %v, want > 0", d)
	}
}

func TestApplyOverride(t *testing.T) {
	base := Config{Mode: ModeNatural, PerChar: 55 * time.Millisecond}

	if got := base.Apply(nil); got != base {
		t.Fatalf("apply(nil) must be a no-op, got %+v", got)
	}

	off := ModeOff
	if got := base.Apply(&Override{Mode: &off}); got.Mode != ModeOff {
		t.Fatalf("apply mode override: got %v, want off", got.Mode)
	}

	pcm, maxMS := 40, 8000
	minc, maxc := 5.0, 8.0
	nat := ModeNatural
	got := base.Apply(&Override{Mode: &nat, PerCharMS: &pcm, MinCPS: &minc, MaxCPS: &maxc, MaxTotalMS: &maxMS})
	switch {
	case got.PerChar != 40*time.Millisecond:
		t.Fatalf("PerChar override: got %v", got.PerChar)
	case got.MinCPS != 5.0 || got.MaxCPS != 8.0:
		t.Fatalf("CPS override: got min=%v max=%v", got.MinCPS, got.MaxCPS)
	case got.MaxTotal != 8000*time.Millisecond:
		t.Fatalf("MaxTotal override: got %v", got.MaxTotal)
	}
}

func TestApplyOverrideAffectsDuration(t *testing.T) {
	base := Config{Mode: ModeNatural}
	off := ModeOff
	if d := Duration("hello", base.Apply(&Override{Mode: &off}), newRNG(1)); d != 0 {
		t.Fatalf("override to off: got %v, want 0", d)
	}
}

func TestNaturalMoreTextTakesLongerOnAverage(t *testing.T) {
	// Not strictly monotonic per-draw (jitter/pauses), but a much longer
	// message should take longer on average across many seeds.
	short, long := "hi", strings.Repeat("satu dua tiga ", 20)
	cfg := Config{Mode: ModeNatural, MaxTotal: time.Minute}
	var sumShort, sumLong time.Duration
	const n = 200
	for seed := uint64(0); seed < n; seed++ {
		sumShort += Duration(short, cfg, newRNG(seed))
		sumLong += Duration(long, cfg, newRNG(seed))
	}
	if sumLong <= sumShort {
		t.Fatalf("expected long avg (%v) > short avg (%v)", sumLong/n, sumShort/n)
	}
}
