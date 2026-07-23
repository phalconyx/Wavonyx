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
	"mime"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phalconyx/wavonyx"
	"github.com/phalconyx/wavonyx/typing"
)

// maxBodyBytes caps JSON request bodies.
const maxBodyBytes = 1 << 20

// Multipart (media send) parsing budgets.
const (
	multipartMemory   = 8 << 20 // in-memory budget before spilling to temp files
	multipartOverhead = 1 << 20 // slack over MaxUploadBytes for fields and boundaries
)

// Config is the server configuration.
type Config struct {
	// APIKey, if non-empty, requires the X-API-Key header on all endpoints
	// except /health.
	APIKey string
	// MaxUploadBytes caps a multipart media upload body. Default: 64 MiB.
	MaxUploadBytes int64
}

// New returns a configured http.Handler backed by api.
func New(api wavonyx.SessionAPI, cfg Config) http.Handler {
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 64 << 20
	}
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
	mux.HandleFunc("GET /sessions/{id}/media", h.downloadMedia)
	mux.HandleFunc("POST /sessions/{id}/messages/{message_id}/edit", h.editMessage)
	mux.HandleFunc("DELETE /sessions/{id}/messages/{message_id}", h.revokeMessage)
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
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		h.sendMediaMessage(w, r)
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

// sendMediaMessage handles a multipart send: form fields to, caption, typing
// (JSON), and a file part. The mimetype is sniffed from the part, the filename,
// then the bytes.
func (h *handler) sendMediaMessage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, h.cfg.MaxUploadBytes+multipartOverhead)
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		writeMediaBodyError(w, r, err)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "missing_file", "missing 'file' part")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeMediaBodyError(w, r, err)
		return
	}

	req := wavonyx.MediaSendRequest{
		To:       r.FormValue("to"),
		Caption:  r.FormValue("caption"),
		Data:     data,
		Filename: header.Filename,
		Mimetype: detectMimetype(header, data),
	}
	if tj := r.FormValue("typing"); tj != "" {
		var ov typing.Override
		if err := json.Unmarshal([]byte(tj), &ov); err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_typing", "typing field is not valid JSON")
			return
		}
		req.Typing = &ov
	}

	res, err := h.api.SendMedia(r.Context(), r.PathValue("id"), req)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusCreated, res)
}

func writeMediaBodyError(w http.ResponseWriter, r *http.Request, err error) {
	if isBodyTooLarge(err) {
		writeError(w, r, http.StatusRequestEntityTooLarge, "media_too_large", "media file exceeds the size limit")
		return
	}
	writeError(w, r, http.StatusBadRequest, "invalid_multipart", "invalid multipart form")
}

func isBodyTooLarge(err error) bool {
	var maxErr *http.MaxBytesError
	return errors.As(err, &maxErr) || (err != nil && strings.Contains(err.Error(), "request body too large"))
}

// detectMimetype resolves an upload's MIME type from the part header, then the
// filename extension, then the content bytes.
func detectMimetype(header *multipart.FileHeader, data []byte) string {
	if ct := cleanMime(header.Header.Get("Content-Type")); ct != "" && ct != "application/octet-stream" {
		return ct
	}
	if mt := cleanMime(mime.TypeByExtension(filepath.Ext(header.Filename))); mt != "" {
		return mt
	}
	return cleanMime(http.DetectContentType(data))
}

func cleanMime(s string) string {
	if i := strings.IndexByte(s, ';'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
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

// downloadMedia streams the decrypted bytes of an inbound attachment referenced
// by the ?token= query parameter. On success it writes raw bytes (no envelope);
// only pre-stream errors use the JSON envelope.
func (h *handler) downloadMedia(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_token", "missing media token")
		return
	}
	content, err := h.api.DownloadMedia(r.Context(), r.PathValue("id"), token)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	ct := content.Mimetype
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+sanitizeFilename(content.Filename)+`"`)
	w.Header().Set("X-Request-Id", requestID(r))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content.Data)
}

// editMessage edits a message previously sent by this session. Body:
// {"to": "<chat>", "text": "<new text>"}; the message id is in the path.
func (h *handler) editMessage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	var body struct {
		To   string `json:"to"`
		Text string `json:"text"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	res, err := h.api.EditMessage(r.Context(), r.PathValue("id"), body.To, r.PathValue("message_id"), body.Text)
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, res)
}

// revokeMessage deletes (for everyone) a message previously sent by this
// session. The chat is taken from ?to= or a JSON body {"to": "<chat>"}.
func (h *handler) revokeMessage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	to := r.URL.Query().Get("to")
	if to == "" {
		var body struct {
			To string `json:"to"`
		}
		if !decodeJSON(w, r, &body) {
			return
		}
		to = body.To
	}
	res, err := h.api.RevokeMessage(r.Context(), r.PathValue("id"), to, r.PathValue("message_id"))
	if err != nil {
		writeAPIError(w, r, err)
		return
	}
	writeData(w, r, http.StatusOK, res)
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
	switch code {
	case "internal":
		msg = "internal error"
	case "send_failed":
		msg = "failed to send message"
	case "media_download_failed":
		msg = "failed to download media"
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
	case errors.Is(err, wavonyx.ErrInvalidToken):
		return http.StatusBadRequest, "invalid_token"
	case errors.Is(err, wavonyx.ErrMissingMedia):
		return http.StatusBadRequest, "missing_media"
	case errors.Is(err, wavonyx.ErrMediaTooLarge):
		return http.StatusRequestEntityTooLarge, "media_too_large"
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
	var me *wavonyx.MediaError
	if errors.As(err, &me) {
		return http.StatusBadGateway, "media_download_failed"
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

// sanitizeFilename strips characters that would break the Content-Disposition
// header, falling back to a default name.
func sanitizeFilename(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, `"`, "")
	if s == "" {
		return "download"
	}
	return s
}
