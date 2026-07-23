package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/phalconyx/wavonyx"
)

// fakeAPI is a configurable in-memory wavonyx.SessionAPI.
type fakeAPI struct {
	info   *wavonyx.SessionInfo
	list   []wavonyx.SessionInfo
	qr     *wavonyx.QRInfo
	result *wavonyx.SendResult
	recent []wavonyx.InboundMessage

	createErr error
	getErr    error
	loginErr  error
	qrErr     error
	logoutErr error
	deleteErr error
	sendErr   error
	recentErr error

	lastCreateID   string
	lastCreateHook string
	lastSendID     string
	lastSendReq    wavonyx.SendRequest
}

var _ wavonyx.SessionAPI = (*fakeAPI)(nil)

func (f *fakeAPI) Create(ctx context.Context, id, webhookURL string) (*wavonyx.SessionInfo, error) {
	f.lastCreateID, f.lastCreateHook = id, webhookURL
	if f.createErr != nil {
		return nil, f.createErr
	}
	return f.info, nil
}
func (f *fakeAPI) List(ctx context.Context) []wavonyx.SessionInfo { return f.list }
func (f *fakeAPI) Get(ctx context.Context, id string) (*wavonyx.SessionInfo, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.info, nil
}
func (f *fakeAPI) Login(ctx context.Context, id string) (*wavonyx.QRInfo, error) {
	if f.loginErr != nil {
		return nil, f.loginErr
	}
	return f.qr, nil
}
func (f *fakeAPI) QR(ctx context.Context, id string) (*wavonyx.QRInfo, error) {
	if f.qrErr != nil {
		return nil, f.qrErr
	}
	return f.qr, nil
}
func (f *fakeAPI) Logout(ctx context.Context, id string) error { return f.logoutErr }
func (f *fakeAPI) Delete(ctx context.Context, id string) error { return f.deleteErr }
func (f *fakeAPI) Send(ctx context.Context, id string, req wavonyx.SendRequest) (*wavonyx.SendResult, error) {
	f.lastSendID, f.lastSendReq = id, req
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	return f.result, nil
}
func (f *fakeAPI) Recent(ctx context.Context, id string, limit int) ([]wavonyx.InboundMessage, error) {
	if f.recentErr != nil {
		return nil, f.recentErr
	}
	return f.recent, nil
}

func do(t *testing.T, h http.Handler, method, path, apiKey, body string) (*http.Response, map[string]any) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var env map[string]any
	_ = json.NewDecoder(res.Body).Decode(&env)
	return res, env
}

func dataObj(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	d, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object in envelope: %v", env)
	}
	return d
}

func errCode(t *testing.T, env map[string]any) string {
	t.Helper()
	e, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("no error object in envelope: %v", env)
	}
	return e["code"].(string)
}

func requireRequestID(t *testing.T, res *http.Response, env map[string]any) {
	t.Helper()
	if res.Header.Get("X-Request-Id") == "" {
		t.Error("missing X-Request-Id header")
	}
	meta, ok := env["meta"].(map[string]any)
	if !ok || meta["request_id"] == "" {
		t.Errorf("missing meta.request_id: %v", env)
	}
}

func TestHealthNoAuth(t *testing.T) {
	h := New(&fakeAPI{}, Config{APIKey: "secret"})
	res, env := do(t, h, "GET", "/health", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if dataObj(t, env)["status"] != "ok" {
		t.Fatalf("health data: %v", env)
	}
	requireRequestID(t, res, env)
}

func TestAuthRequired(t *testing.T) {
	h := New(&fakeAPI{}, Config{APIKey: "secret"})
	if res, env := do(t, h, "GET", "/sessions", "", ""); res.StatusCode != http.StatusUnauthorized || errCode(t, env) != "unauthorized" {
		t.Fatalf("no key: status=%d env=%v", res.StatusCode, env)
	}
	if res, _ := do(t, h, "GET", "/sessions", "secret", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("with key: status=%d", res.StatusCode)
	}
}

func TestCreateSession(t *testing.T) {
	f := &fakeAPI{info: &wavonyx.SessionInfo{ID: "personal", Status: wavonyx.StatusCreated, CreatedAt: time.Unix(1_700_000_000, 0).UTC()}}
	h := New(f, Config{})
	res, env := do(t, h, "POST", "/sessions", "", `{"id":"personal","webhook_url":"https://hook"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
	if f.lastCreateID != "personal" || f.lastCreateHook != "https://hook" {
		t.Fatalf("create args: id=%q hook=%q", f.lastCreateID, f.lastCreateHook)
	}
	if dataObj(t, env)["id"] != "personal" {
		t.Fatalf("data: %v", env)
	}
	requireRequestID(t, res, env)
}

func TestSendMessage(t *testing.T) {
	f := &fakeAPI{result: &wavonyx.SendResult{MessageID: "3EB0", To: "628@s.whatsapp.net"}}
	h := New(f, Config{})
	res, env := do(t, h, "POST", "/sessions/personal/messages", "", `{"to":"628123","text":"halo"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
	if f.lastSendID != "personal" || f.lastSendReq.Text != "halo" || f.lastSendReq.To != "628123" {
		t.Fatalf("send args: id=%q req=%+v", f.lastSendID, f.lastSendReq)
	}
	if dataObj(t, env)["message_id"] != "3EB0" {
		t.Fatalf("data: %v", env)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		configure  func(*fakeAPI)
		wantStatus int
		wantCode   string
	}{
		{"not found", "GET", "/sessions/x", "", func(f *fakeAPI) { f.getErr = wavonyx.ErrSessionNotFound }, 404, "session_not_found"},
		{"exists", "POST", "/sessions", `{"id":"x"}`, func(f *fakeAPI) { f.createErr = wavonyx.ErrSessionExists }, 409, "session_exists"},
		{"invalid id", "POST", "/sessions", `{"id":"BAD"}`, func(f *fakeAPI) { f.createErr = wavonyx.ErrInvalidSessionID }, 400, "invalid_session_id"},
		{"already connected", "POST", "/sessions/x/login", "", func(f *fakeAPI) { f.loginErr = wavonyx.ErrAlreadyConnected }, 409, "already_connected"},
		{"qr not available", "GET", "/sessions/x/qr", "", func(f *fakeAPI) { f.qrErr = wavonyx.ErrQRNotAvailable }, 404, "qr_not_available"},
		{"not connected", "POST", "/sessions/x/messages", `{"to":"628123","text":"hi"}`, func(f *fakeAPI) { f.sendErr = wavonyx.ErrNotConnected }, 409, "not_connected"},
		{"empty text", "POST", "/sessions/x/messages", `{"to":"628123","text":""}`, func(f *fakeAPI) { f.sendErr = wavonyx.ErrEmptyText }, 400, "missing_text"},
		{"queue full", "POST", "/sessions/x/messages", `{"to":"628123","text":"hi"}`, func(f *fakeAPI) { f.sendErr = wavonyx.ErrQueueFull }, 429, "queue_full"},
		{"send failed", "POST", "/sessions/x/messages", `{"to":"628123","text":"hi"}`, func(f *fakeAPI) { f.sendErr = &wavonyx.SendError{To: "628", Err: io.EOF} }, 502, "send_failed"},
		{"not logged in", "POST", "/sessions/x/logout", "", func(f *fakeAPI) { f.logoutErr = wavonyx.ErrNotLoggedIn }, 409, "not_logged_in"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			tc.configure(f)
			h := New(f, Config{})
			res, env := do(t, h, tc.method, tc.path, "", tc.body)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status=%d want %d env=%v", res.StatusCode, tc.wantStatus, env)
			}
			if got := errCode(t, env); got != tc.wantCode {
				t.Fatalf("code=%q want %q", got, tc.wantCode)
			}
			requireRequestID(t, res, env)
		})
	}
}

func TestInvalidJSON(t *testing.T) {
	h := New(&fakeAPI{}, Config{})
	res, env := do(t, h, "POST", "/sessions", "", "{not json")
	if res.StatusCode != http.StatusBadRequest || errCode(t, env) != "invalid_json" {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
}

func TestInvalidLimit(t *testing.T) {
	h := New(&fakeAPI{}, Config{})
	res, env := do(t, h, "GET", "/sessions/x/messages?limit=abc", "", "")
	if res.StatusCode != http.StatusBadRequest || errCode(t, env) != "invalid_limit" {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
}

func TestUnknownRoute(t *testing.T) {
	h := New(&fakeAPI{}, Config{})
	res, env := do(t, h, "GET", "/does/not/exist", "", "")
	if res.StatusCode != http.StatusNotFound || errCode(t, env) != "unknown_route" {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
}

func TestRequestIDEchoedAndSanitized(t *testing.T) {
	h := New(&fakeAPI{}, Config{})
	req := httptest.NewRequest("GET", "/health", nil)
	req.Header.Set("X-Request-Id", "abc-123_bad\r\ninject")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	got := rec.Result().Header.Get("X-Request-Id")
	if got != "abc-123_badinject" {
		t.Fatalf("sanitized request id = %q", got)
	}
}
