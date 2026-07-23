// Package typing computes how long to display a "typing…" (composing) indicator
// before sending a WhatsApp message, so outgoing messages are paced like a human
// rather than fired instantly — a simple mitigation against being flagged as a
// bot.
//
// [Duration] is a pure function: given the message text, a [Config], and a
// random source, it returns a single delay. The random source is injected so
// the natural mode is fully deterministic under a seeded generator, which keeps
// it unit-testable. The caller (the send worker) is responsible for actually
// sleeping for the returned duration while re-sending the composing presence.
package typing

import (
	"math/rand/v2"
	"time"
	"unicode"
	"unicode/utf8"
)

// Mode selects how the typing delay is computed.
type Mode string

const (
	// ModeOff disables the typing indicator: [Duration] always returns 0.
	ModeOff Mode = "off"
	// ModeConstant uses a fixed delay per rune (Config.PerChar), clamped to
	// [Config.MinTotal, Config.MaxTotal].
	ModeConstant Mode = "constant"
	// ModeNatural draws a human-like delay: a randomized typing speed per
	// message, per-word jitter, and occasional pauses after sentence endings.
	ModeNatural Mode = "natural"
)

// Package defaults, applied to any zero-valued Config field.
const (
	defaultPerChar  = 55 * time.Millisecond
	defaultMinCPS   = 4.0
	defaultMaxCPS   = 9.0
	defaultMinTotal = 400 * time.Millisecond
	defaultMaxTotal = 15 * time.Second
)

// Tuning constants for ModeNatural. These are intentionally not configurable —
// they shape the "feel" of natural typing, while Config exposes only the knobs
// worth tuning per deployment (speed range and total clamps).
const (
	naturalReadMinMS  = 300  // pre-typing "read/think" delay, lower bound
	naturalReadMaxMS  = 900  // pre-typing "read/think" delay, upper bound
	naturalJitterLo   = 0.80 // per-word speed multiplier, lower bound
	naturalJitterHi   = 1.25 // per-word speed multiplier, upper bound
	naturalPauseProb  = 0.60 // chance of a pause after a sentence ending
	naturalPauseMinMS = 350  // sentence pause, lower bound
	naturalPauseMaxMS = 900  // sentence pause, upper bound
)

// Config controls how [Duration] computes the delay. The zero value is valid:
// every unset field falls back to a package default, and an empty Mode defaults
// to ModeNatural.
type Config struct {
	Mode Mode

	// PerChar is the delay per rune in ModeConstant. Default: 55ms.
	PerChar time.Duration

	// MinCPS and MaxCPS bound the randomized typing speed (characters per
	// second) in ModeNatural. Defaults: 4.0 and 9.0.
	MinCPS float64
	MaxCPS float64

	// MinTotal and MaxTotal clamp the final duration. MaxTotal is the hard
	// stall guard that keeps long messages from blocking the send queue.
	// Defaults: 400ms and 15s.
	MinTotal time.Duration
	MaxTotal time.Duration
}

// Default returns a Config populated with the package defaults (natural mode).
// It is a convenience for callers that want a fully-populated starting point,
// e.g. the service reading WAVONYX_TYPING_* environment variables.
func Default() Config {
	return Config{
		Mode:     ModeNatural,
		PerChar:  defaultPerChar,
		MinCPS:   defaultMinCPS,
		MaxCPS:   defaultMaxCPS,
		MinTotal: defaultMinTotal,
		MaxTotal: defaultMaxTotal,
	}
}

// Override is a set of optional, per-request tweaks to a base Config. A nil
// pointer field leaves the base value untouched. Durations are expressed in
// milliseconds so they map cleanly onto a JSON request body.
type Override struct {
	Mode       *Mode    `json:"mode,omitempty"`
	PerCharMS  *int     `json:"per_char_ms,omitempty"`
	MinCPS     *float64 `json:"min_cps,omitempty"`
	MaxCPS     *float64 `json:"max_cps,omitempty"`
	MaxTotalMS *int     `json:"max_total_ms,omitempty"`
}

// Apply returns c with the non-nil fields of o applied on top. It does not
// mutate c and treats a nil override as a no-op.
func (c Config) Apply(o *Override) Config {
	if o == nil {
		return c
	}
	if o.Mode != nil {
		c.Mode = *o.Mode
	}
	if o.PerCharMS != nil {
		c.PerChar = time.Duration(*o.PerCharMS) * time.Millisecond
	}
	if o.MinCPS != nil {
		c.MinCPS = *o.MinCPS
	}
	if o.MaxCPS != nil {
		c.MaxCPS = *o.MaxCPS
	}
	if o.MaxTotalMS != nil {
		c.MaxTotal = time.Duration(*o.MaxTotalMS) * time.Millisecond
	}
	return c
}

// withDefaults returns c with every unset field filled from the package
// defaults and the speed/clamp bounds normalized so MaxCPS >= MinCPS and
// MaxTotal >= MinTotal.
func (c Config) withDefaults() Config {
	if c.Mode == "" {
		c.Mode = ModeNatural
	}
	if c.PerChar <= 0 {
		c.PerChar = defaultPerChar
	}
	if c.MinCPS <= 0 {
		c.MinCPS = defaultMinCPS
	}
	if c.MaxCPS <= 0 {
		c.MaxCPS = defaultMaxCPS
	}
	if c.MaxCPS < c.MinCPS {
		c.MaxCPS = c.MinCPS
	}
	if c.MinTotal <= 0 {
		c.MinTotal = defaultMinTotal
	}
	if c.MaxTotal <= 0 {
		c.MaxTotal = defaultMaxTotal
	}
	if c.MaxTotal < c.MinTotal {
		c.MaxTotal = c.MinTotal
	}
	return c
}

// Duration returns how long to show the typing indicator before sending text.
//
// It is pure with respect to rng: the same text, config, and equally-seeded rng
// always yield the same result. rng is read only in ModeNatural; ModeOff and
// ModeConstant ignore it and accept a nil rng. As a safety net, a nil rng in
// ModeNatural falls back to a time-seeded source rather than panicking.
func Duration(text string, cfg Config, rng *rand.Rand) time.Duration {
	if text == "" {
		return 0
	}
	cfg = cfg.withDefaults()

	switch cfg.Mode {
	case ModeOff:
		return 0
	case ModeConstant:
		n := utf8.RuneCountInString(text)
		return clamp(time.Duration(n)*cfg.PerChar, cfg.MinTotal, cfg.MaxTotal)
	case ModeNatural:
		return naturalDuration(text, cfg, rng)
	default:
		return 0
	}
}

// naturalDuration models human typing: a per-message base speed, per-word jitter
// around it, a short initial read delay, and occasional pauses after sentence
// endings — all clamped to [MinTotal, MaxTotal].
func naturalDuration(text string, cfg Config, rng *rand.Rand) time.Duration {
	if rng == nil {
		rng = rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 0x9E3779B97F4A7C15))
	}

	cps := cfg.MinCPS + rng.Float64()*(cfg.MaxCPS-cfg.MinCPS)
	if cps <= 0 {
		cps = 1
	}

	// Initial "read the conversation and start typing" delay.
	total := uniformMS(rng, naturalReadMinMS, naturalReadMaxMS)

	runes := []rune(text)
	n := len(runes)
	for i := 0; i < n; {
		start := i
		for i < n && !unicode.IsSpace(runes[i]) {
			i++
		}
		wordEnd := i
		sawNewline := false
		for i < n && unicode.IsSpace(runes[i]) {
			if runes[i] == '\n' {
				sawNewline = true
			}
			i++
		}

		chunkLen := wordEnd - start
		if chunkLen == 0 {
			continue // leading/duplicate whitespace
		}

		jitter := naturalJitterLo + rng.Float64()*(naturalJitterHi-naturalJitterLo)
		total += time.Duration(float64(chunkLen) / cps * jitter * float64(time.Second))

		// Pause "to think" after a sentence ending, but only when more text
		// follows — no point pausing at the very end.
		if i < n && (sawNewline || isSentenceEnd(runes[wordEnd-1])) {
			if rng.Float64() < naturalPauseProb {
				total += uniformMS(rng, naturalPauseMinMS, naturalPauseMaxMS)
			}
		}
	}

	return clamp(total, cfg.MinTotal, cfg.MaxTotal)
}

// uniformMS returns a uniformly random duration in [loMS, hiMS] milliseconds.
func uniformMS(rng *rand.Rand, loMS, hiMS int) time.Duration {
	if hiMS <= loMS {
		return time.Duration(loMS) * time.Millisecond
	}
	return time.Duration(loMS+rng.IntN(hiMS-loMS+1)) * time.Millisecond
}

// isSentenceEnd reports whether r ends a sentence (Latin and CJK forms).
func isSentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	}
	return false
}

// clamp bounds d to [lo, hi]. The floor is applied last so an explicit MinTotal
// is always honored even if it exceeds hi.
func clamp(d, lo, hi time.Duration) time.Duration {
	if hi > 0 && d > hi {
		d = hi
	}
	if d < lo {
		d = lo
	}
	return d
}
