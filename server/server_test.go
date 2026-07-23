package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
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
	media  *wavonyx.MediaContent

	createErr   error
	getErr      error
	loginErr    error
	qrErr       error
	logoutErr   error
	deleteErr   error
	sendErr     error
	recentErr   error
	downloadErr error

	lastCreateID   string
	lastCreateHook string
	lastSendID     string
	lastSendReq    wavonyx.SendRequest
	lastMediaReq   wavonyx.MediaSendRequest
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
func (f *fakeAPI) SendMedia(ctx context.Context, id string, req wavonyx.MediaSendRequest) (*wavonyx.SendResult, error) {
	f.lastSendID, f.lastMediaReq = id, req
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
func (f *fakeAPI) DownloadMedia(ctx context.Context, id, token string) (*wavonyx.MediaContent, error) {
	if f.downloadErr != nil {
		return nil, f.downloadErr
	}
	return f.media, nil
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

func TestDownloadMediaEndpoint(t *testing.T) {
	f := &fakeAPI{media: &wavonyx.MediaContent{Data: []byte("JPEGBYTES"), Mimetype: "image/jpeg", Filename: "photo.jpg"}}
	h := New(f, Config{})

	// Success streams raw bytes (no envelope) with hardening headers.
	req := httptest.NewRequest("GET", "/sessions/personal/media?token=abc", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("content-type: %q", ct)
	}
	if cd := res.Header.Get("Content-Disposition"); !strings.Contains(cd, "photo.jpg") {
		t.Fatalf("disposition: %q", cd)
	}
	if res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
	if body, _ := io.ReadAll(res.Body); string(body) != "JPEGBYTES" {
		t.Fatalf("body: %q", body)
	}

	// Missing token -> 400 invalid_token (enveloped).
	if res, env := do(t, h, "GET", "/sessions/personal/media", "", ""); res.StatusCode != http.StatusBadRequest || errCode(t, env) != "invalid_token" {
		t.Fatalf("missing token: status=%d env=%v", res.StatusCode, env)
	}

	// Download failure -> 502 media_download_failed.
	h2 := New(&fakeAPI{downloadErr: &wavonyx.MediaError{Err: io.EOF}}, Config{})
	if res, env := do(t, h2, "GET", "/sessions/personal/media?token=abc", "", ""); res.StatusCode != http.StatusBadGateway || errCode(t, env) != "media_download_failed" {
		t.Fatalf("download err: status=%d env=%v", res.StatusCode, env)
	}
}

func TestSendMediaMultipart(t *testing.T) {
	f := &fakeAPI{result: &wavonyx.SendResult{MessageID: "MM1", To: "628@s.whatsapp.net"}}
	h := New(f, Config{})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("to", "628123")
	_ = mw.WriteField("caption", "hello pic")
	part, _ := mw.CreateFormFile("file", "photo.jpg")
	_, _ = part.Write([]byte("\xff\xd8\xffJPEGDATA"))
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/sessions/personal/messages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("status=%d body=%s", res.StatusCode, body)
	}
	if f.lastSendID != "personal" || f.lastMediaReq.To != "628123" || f.lastMediaReq.Caption != "hello pic" {
		t.Fatalf("media req: id=%q %+v", f.lastSendID, f.lastMediaReq)
	}
	if f.lastMediaReq.Filename != "photo.jpg" || !strings.HasPrefix(f.lastMediaReq.Mimetype, "image/") {
		t.Fatalf("filename/mimetype: %q / %q", f.lastMediaReq.Filename, f.lastMediaReq.Mimetype)
	}
	if string(f.lastMediaReq.Data) != "\xff\xd8\xffJPEGDATA" {
		t.Fatalf("data: %q", f.lastMediaReq.Data)
	}
}

func TestSendMediaMissingFile(t *testing.T) {
	h := New(&fakeAPI{}, Config{})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("to", "628123")
	_ = mw.Close()

	req := httptest.NewRequest("POST", "/sessions/personal/messages", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	var env map[string]any
	_ = json.NewDecoder(res.Body).Decode(&env)
	if res.StatusCode != http.StatusBadRequest || errCode(t, env) != "missing_file" {
		t.Fatalf("status=%d env=%v", res.StatusCode, env)
	}
}
