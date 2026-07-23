// Package server exposes Wavonyx as an HTTP service.
//
// All JSON responses share a consistent envelope:
//
//	success: {"data": <payload>, "meta": {"request_id": "..."}}
//	error:   {"error": {"code": "...", "message": "...", "details": {...}?}, "meta": {"request_id": "..."}}
//
// The HTTP status code is authoritative; the body never contradicts it. Every
// response carries an X-Request-Id header echoing meta.request_id.
//
// Endpoints (all except /health require X-API-Key when an API key is set):
//
//	GET    /health                       - liveness check
//	POST   /sessions                     - create a session {"id"?, "webhook_url"?}
//	GET    /sessions                     - list sessions
//	GET    /sessions/{id}                - inspect a session
//	POST   /sessions/{id}/login          - start pairing, returns the first QR code
//	GET    /sessions/{id}/qr             - latest rotating QR code
//	POST   /sessions/{id}/logout         - unlink the device
//	DELETE /sessions/{id}                - delete the session entirely
//	POST   /sessions/{id}/messages       - send a text message {"to", "text", "typing"?}
//	GET    /sessions/{id}/messages?limit - recent inbound messages (ring buffer)
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/phalconyx/wavonyx"
)

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 1 << 20

// Config is the server configuration.
type Config struct {
	// APIKey, if non-empty, requires the X-API-Key header on all endpoints
	// except /health.
	APIKey string
}

// New returns a configured http.Handler backed by api.
func New(api wavonyx.SessionAPI, cfg Config) http.Handler {
	h := &handler{api: api, cfg: cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("POST /sessions", h.createSession)
	mux.HandleFunc("GET /sessions", h.listSessions)
	mux.HandleFunc("GET /sessions/{id}", h.getSession)
	mux.HandleFunc("POST /sessions/{id}/login", h.login)
	mux.HandleFunc("GET /sessions/{id}/qr", h.qr)
	mux.HandleFunc("POST /sessions/{id}/logout", h.logout)
	mux.HandleFunc("DELETE /sessions/{id}", h.deleteSession)
	mux.HandleFunc("POST /sessions/{id}/messages", h.sendMessage)
	mux.HandleFunc("GET /sessions/{id}/messages", h.recentMessages)
	mux.HandleFunc("/", h.notFound)
	return mux
}

type handler struct {
	api wavonyx.SessionAPI
	cfg Config
}

func (h *handler) auth(w http.ResponseWriter, r *http.Request) bool {
	if h.cfg.APIKey == "" {
		return true
	}
	got := r.Header.Get("X-API-Key")
	if subtle.ConstantTimeCompare([]byte(got), []byte(h.cfg.APIKey)) != 1 {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "missing or invalid X-API-Key")
		return false
	}
	return true
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	writeData(w, r, http.StatusOK, map[string]any{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
}

func (h *handler) createSession(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	var body struct {
		ID         string `json:"id"`
		WebhookURL string `json:"webhook_url"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	info, err := h.api.Create(r.Context(), body.ID, body.WebhookURL)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusCreated, info)
}

func (h *handler) listSessions(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{"sessions": h.api.List(r.Context())})
}

func (h *handler) getSession(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	info, err := h.api.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, info)
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	qr, err := h.api.Login(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{
		"status": string(wavonyx.StatusPairing),
		"qr":     qr,
	})
}

func (h *handler) qr(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	qr, err := h.api.QR(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{"qr": qr})
}

func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	id := r.PathValue("id")
	if err := h.api.Logout(r.Context(), id); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{"id": id, "status": string(wavonyx.StatusLoggedOut)})
}

func (h *handler) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	if err := h.api.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, map[string]any{"deleted": true})
}

func (h *handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	var req wavonyx.SendRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := h.api.Send(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusCreated, res)
}

func (h *handler) recentMessages(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_limit", "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	msgs, err := h.api.Recent(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	if msgs == nil {
		msgs = []wavonyx.InboundMessage{}
	}
	writeData(w, r, http.StatusOK, map[string]any{"messages": msgs, "count": len(msgs)})
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, r, http.StatusNotFound, "unknown_route", "no such endpoint")
}

// decodeJSON reads and decodes a size-limited JSON body into dst. An empty body
// is treated as an empty object. It writes an invalid_json error and returns
// false on malformed input.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true
		}
		writeError(w, r, http.StatusBadRequest, "invalid_json", "request body is not valid JSON")
		return false
	}
	return true
}

// writeAPIError maps a wavonyx error to the appropriate status and stable code.
func writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := classify(err)
	msg := err.Error()
	switch status {
	case http.StatusInternalServerError:
		msg = "internal error"
	case http.StatusBadGateway:
		msg = "failed to send message"
	}
	writeError(w, r, status, code, msg)
}

func classify(err error) (int, string) {
	switch {
	case errors.Is(err, wavonyx.ErrInvalidSessionID):
		return http.StatusBadRequest, "invalid_session_id"
	case errors.Is(err, wavonyx.ErrEmptyText):
		return http.StatusBadRequest, "missing_text"
	case errors.Is(err, wavonyx.ErrInvalidRecipient):
		return http.StatusBadRequest, "invalid_recipient"
	case errors.Is(err, wavonyx.ErrInvalidTyping):
		return http.StatusBadRequest, "invalid_typing"
	case errors.Is(err, wavonyx.ErrSessionNotFound):
		return http.StatusNotFound, "session_not_found"
	case errors.Is(err, wavonyx.ErrQRNotAvailable):
		return http.StatusNotFound, "qr_not_available"
	case errors.Is(err, wavonyx.ErrSessionExists):
		return http.StatusConflict, "session_exists"
	case errors.Is(err, wavonyx.ErrAlreadyConnected):
		return http.StatusConflict, "already_connected"
	case errors.Is(err, wavonyx.ErrNotConnected):
		return http.StatusConflict, "not_connected"
	case errors.Is(err, wavonyx.ErrNotLoggedIn):
		return http.StatusConflict, "not_logged_in"
	case errors.Is(err, wavonyx.ErrQueueFull):
		return http.StatusTooManyRequests, "queue_full"
	}
	var se *wavonyx.SendError
	if errors.As(err, &se) {
		return http.StatusBadGateway, "send_failed"
	}
	return http.StatusInternalServerError, "internal"
}

// writeData writes a success envelope: {"data": <data>, "meta": {"request_id": ...}}.
func writeData(w http.ResponseWriter, r *http.Request, code int, data any) {
	rid := requestID(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", rid)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": data,
		"meta": map[string]any{"request_id": rid},
	})
}

// writeError writes an error envelope with a machine-readable code and a
// human-readable message. The HTTP status code carries the error category.
func writeError(w http.ResponseWriter, r *http.Request, code int, errCode, message string) {
	rid := requestID(r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", rid)
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": errCode, "message": message},
		"meta":  map[string]any{"request_id": rid},
	})
}

// requestID returns a correlation id: the client-supplied X-Request-Id
// (sanitized) if present, otherwise a freshly generated one.
func requestID(r *http.Request) string {
	if id := sanitizeRequestID(r.Header.Get("X-Request-Id")); id != "" {
		return id
	}
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(b[:])
}

// sanitizeRequestID keeps only header-safe characters and caps the length, so a
// client-supplied id can be echoed back without enabling header injection.
func sanitizeRequestID(s string) string {
	if len(s) > 128 {
		s = s[:128]
	}
	return strings.Map(func(rn rune) rune {
		switch {
		case rn >= 'a' && rn <= 'z', rn >= 'A' && rn <= 'Z', rn >= '0' && rn <= '9':
			return rn
		case rn == '-', rn == '_', rn == '.':
			return rn
		default:
			return -1
		}
	}, s)
}
