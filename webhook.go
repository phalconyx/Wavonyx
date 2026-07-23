package wavonyx

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	rand "math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

// WebhookConfig configures webhook delivery. A zero URL disables delivery.
type WebhookConfig struct {
	URL    string // global default endpoint; a per-session override wins
	Secret string // HMAC-SHA256 key; empty means unsigned

	Timeout     time.Duration // per-attempt HTTP timeout (default 10s)
	Retries     int           // total attempts (default 5)
	BackoffBase time.Duration // first retry delay (default 500ms)
	BackoffMax  time.Duration // retry delay ceiling (default 30s)
	Workers     int           // delivery goroutines; 1 = strict FIFO (default 1)
	QueueSize   int           // buffered events before dropping (default 1024)
}

func (c WebhookConfig) withDefaults() WebhookConfig {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.Retries <= 0 {
		c.Retries = 5
	}
	if c.BackoffBase <= 0 {
		c.BackoffBase = 500 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.Workers <= 0 {
		c.Workers = 1
	}
	if c.QueueSize <= 0 {
		c.QueueSize = 1024
	}
	return c
}

type webhookJob struct {
	url      string
	delivery string
	event    string
	session  string
	body     []byte
}

// Dispatcher delivers events to webhook endpoints with HMAC signing and
// bounded, jittered retries. Events are queued and delivered asynchronously;
// when the queue is full they are dropped (the per-session ring buffer remains
// the durable-enough fallback for debugging).
type Dispatcher struct {
	cfg    WebhookConfig
	log    *slog.Logger
	client *http.Client

	jobs   chan webhookJob
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	dropped atomic.Int64
}

// NewDispatcher starts a Dispatcher with cfg.Workers delivery goroutines.
func NewDispatcher(cfg WebhookConfig, log *slog.Logger) *Dispatcher {
	cfg = cfg.withDefaults()
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Dispatcher{
		cfg:    cfg,
		log:    log,
		client: &http.Client{Timeout: cfg.Timeout},
		jobs:   make(chan webhookJob, cfg.QueueSize),
		ctx:    ctx,
		cancel: cancel,
	}
	d.wg.Add(cfg.Workers)
	for i := 0; i < cfg.Workers; i++ {
		go d.worker()
	}
	return d
}

// Enqueue schedules ev for delivery. url is the per-session override; when empty
// the global WebhookConfig.URL is used, and when both are empty the event is
// dropped silently (webhooks disabled). Enqueue never blocks: if the queue is
// full the event is counted in Dropped and discarded.
func (d *Dispatcher) Enqueue(url string, ev Event) {
	if url == "" {
		url = d.cfg.URL
	}
	if url == "" {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		d.log.Warn("webhook marshal failed", "session", ev.SessionID, "err", err)
		return
	}
	job := webhookJob{
		url:      url,
		delivery: newDeliveryID(),
		event:    ev.Event,
		session:  ev.SessionID,
		body:     body,
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	select {
	case d.jobs <- job:
	default:
		d.dropped.Add(1)
		d.log.Warn("webhook queue full, dropping event", "session", ev.SessionID, "event", ev.Event)
	}
}

// Dropped returns the number of events discarded because the queue was full.
func (d *Dispatcher) Dropped() int64 { return d.dropped.Load() }

// Close stops accepting events and drains in-flight deliveries, waiting up to
// ~5s before forcibly cancelling.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	close(d.jobs)
	d.mu.Unlock()

	done := make(chan struct{})
	go func() { d.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		d.cancel() // abort lingering retries
		<-done
	}
	d.cancel()
}

func (d *Dispatcher) worker() {
	defer d.wg.Done()
	for job := range d.jobs {
		d.deliver(job)
	}
}

func (d *Dispatcher) deliver(job webhookJob) {
	var lastErr error
	for attempt := 0; attempt < d.cfg.Retries; attempt++ {
		if d.ctx.Err() != nil {
			return
		}
		retryable, err := d.attempt(job)
		if err == nil {
			return
		}
		lastErr = err
		if !retryable {
			d.log.Warn("webhook delivery failed",
				"url", safeURL(job.url), "session", job.session, "delivery", job.delivery, "err", err)
			return
		}
		if attempt == d.cfg.Retries-1 {
			break
		}
		select {
		case <-d.ctx.Done():
			return
		case <-time.After(backoff(attempt, d.cfg.BackoffBase, d.cfg.BackoffMax)):
		}
	}
	d.log.Warn("webhook delivery gave up",
		"url", safeURL(job.url), "session", job.session, "delivery", job.delivery,
		"attempts", d.cfg.Retries, "err", lastErr)
}

// attempt performs one delivery. It returns retryable=true for transport errors,
// 5xx, and 429; other non-2xx responses are permanent failures.
func (d *Dispatcher) attempt(job webhookJob) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(d.ctx, http.MethodPost, job.url, bytes.NewReader(job.body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "wavonyx-webhook/1")
	req.Header.Set("X-Wavonyx-Event", job.event)
	req.Header.Set("X-Wavonyx-Session", job.session)
	req.Header.Set("X-Wavonyx-Delivery", job.delivery)
	if d.cfg.Secret != "" {
		req.Header.Set("X-Wavonyx-Signature", "sha256="+sign(d.cfg.Secret, job.body))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return true, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096)) // drain for keep-alive reuse

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return true, fmt.Errorf("webhook returned status %d", resp.StatusCode)
	default:
		return false, fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}
}

// sign returns the lowercase hex HMAC-SHA256 of body under secret.
func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// safeURL strips the query and any userinfo so a webhook URL can be logged
// without leaking secrets embedded in it.
func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<invalid-url>"
	}
	return u.Scheme + "://" + u.Host + u.Path
}

func newDeliveryID() string {
	return fmt.Sprintf("whk_%016x%016x", rand.Uint64(), rand.Uint64())
}

// backoff returns a delay with exponential growth and ±50% jitter, capped at
// max. It mirrors the retry backoff used across the sibling projects.
func backoff(attempt int, base, max time.Duration) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	shift := attempt
	if shift > 12 {
		shift = 12
	}
	d := base << shift
	if d <= 0 || d > max {
		d = max
	}
	half := int64(d) / 2
	if half <= 0 {
		return 0
	}
	return time.Duration(half + rand.Int64N(half+1))
}
