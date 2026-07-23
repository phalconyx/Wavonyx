package wavonyx

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type capturedReq struct {
	headers http.Header
	body    []byte
}

func hmacHex(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

func testEvent() Event {
	return Event{
		Event:     EventMessage,
		SessionID: "personal",
		TS:        time.Unix(1_700_000_000, 0).UTC(),
		Data:      InboundMessage{MessageID: "M1", Text: "halo", Chat: "628@s.whatsapp.net"},
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(d):
		t.Fatal("timed out waiting for channel")
		panic("unreachable")
	}
}

func TestWebhookDeliversWithSignature(t *testing.T) {
	got := make(chan capturedReq, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- capturedReq{headers: r.Header.Clone(), body: b}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(WebhookConfig{URL: srv.URL, Secret: "s3cr3t"}, nil)
	defer d.Close()

	ev := testEvent()
	d.Enqueue("", ev)

	req := waitFor(t, got, 2*time.Second)
	if ct := req.headers.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type: %q", ct)
	}
	if req.headers.Get("X-Wavonyx-Event") != EventMessage || req.headers.Get("X-Wavonyx-Session") != "personal" {
		t.Fatalf("event headers: %+v", req.headers)
	}
	if req.headers.Get("X-Wavonyx-Delivery") == "" {
		t.Fatal("missing delivery header")
	}
	wantSig := "sha256=" + hmacHex("s3cr3t", req.body)
	if got := req.headers.Get("X-Wavonyx-Signature"); got != wantSig {
		t.Fatalf("signature mismatch:\n got  %s\n want %s", got, wantSig)
	}
	var decoded Event
	if err := json.Unmarshal(req.body, &decoded); err != nil {
		t.Fatalf("body not valid Event JSON: %v", err)
	}
	if decoded.Data.MessageID != "M1" || decoded.SessionID != "personal" {
		t.Fatalf("decoded event: %+v", decoded)
	}
}

func TestWebhookNoSignatureWhenSecretEmpty(t *testing.T) {
	got := make(chan capturedReq, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- capturedReq{headers: r.Header.Clone(), body: b}
	}))
	defer srv.Close()

	d := NewDispatcher(WebhookConfig{URL: srv.URL}, nil)
	defer d.Close()
	d.Enqueue("", testEvent())

	req := waitFor(t, got, 2*time.Second)
	if sig := req.headers.Get("X-Wavonyx-Signature"); sig != "" {
		t.Fatalf("unexpected signature without secret: %q", sig)
	}
}

func TestWebhookRetriesThenSucceeds(t *testing.T) {
	var calls int32
	done := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		select {
		case done <- struct{}{}:
		default:
		}
	}))
	defer srv.Close()

	d := NewDispatcher(WebhookConfig{
		URL: srv.URL, Retries: 5,
		BackoffBase: time.Millisecond, BackoffMax: 5 * time.Millisecond,
	}, nil)
	defer d.Close()
	d.Enqueue("", testEvent())

	waitFor(t, done, 2*time.Second)
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("calls=%d want 3 (two 500s then 200)", n)
	}
}

func TestWebhookNoRetryOn4xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d := NewDispatcher(WebhookConfig{URL: srv.URL, Retries: 5, BackoffBase: time.Millisecond}, nil)
	d.Enqueue("", testEvent())
	d.Close() // drains

	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("calls=%d want 1 (400 is permanent)", n)
	}
}

func TestWebhookDropsWhenQueueFull(t *testing.T) {
	entered := make(chan struct{}, 10)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := NewDispatcher(WebhookConfig{URL: srv.URL, Workers: 1, QueueSize: 1}, nil)

	d.Enqueue("", testEvent()) // taken by the worker, which blocks in the server
	waitFor(t, entered, 2*time.Second)
	d.Enqueue("", testEvent()) // fills the queue (size 1)
	d.Enqueue("", testEvent()) // dropped
	d.Enqueue("", testEvent()) // dropped

	if got := d.Dropped(); got != 2 {
		t.Fatalf("dropped=%d want 2", got)
	}
	close(release)
	d.Close()
}

func TestWebhookNoURLIsNoop(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer srv.Close()

	// No global URL and no per-session override: nothing should be sent.
	d := NewDispatcher(WebhookConfig{}, nil)
	d.Enqueue("", testEvent())
	time.Sleep(50 * time.Millisecond)
	d.Close()

	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Fatalf("calls=%d want 0", n)
	}
	if got := d.Dropped(); got != 0 {
		t.Fatalf("dropped=%d want 0 (disabled, not dropped)", got)
	}
}

func TestSafeURLRedactsQueryAndUserinfo(t *testing.T) {
	cases := map[string]string{
		"https://u:p@hooks.example.com/wh?token=secret": "https://hooks.example.com/wh",
		"http://host/path?a=b":                          "http://host/path",
	}
	for in, want := range cases {
		if got := safeURL(in); got != want {
			t.Fatalf("safeURL(%q)=%q want %q", in, got, want)
		}
	}
}
