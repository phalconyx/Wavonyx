package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/phalconyx/wavonyx"
)

// client is a thin HTTP client for a running `wavonyx serve` instance. The CLI
// never opens WhatsApp sessions itself: those are long-lived connections owned
// by the server, and two processes sharing one device store would conflict.
type client struct {
	baseURL string
	apiKey  string
	hc      *http.Client
}

func newClient(baseURL, apiKey string, timeout time.Duration) *client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &client{baseURL: baseURL, apiKey: apiKey, hc: &http.Client{Timeout: timeout}}
}

// apiError carries a server error envelope.
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("server returned status %d", e.Status)
	}
	return fmt.Sprintf("%s [%s]", e.Message, e.Code)
}

// codeIs reports whether err is an apiError with the given stable error code.
func codeIs(err error, code string) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == code
}

// resolveBaseURL picks the server address in order: the --url flag, WAVONYX_URL,
// the listen address in WAVONYX_ADDR, then the default local port.
func resolveBaseURL(flagURL string) string {
	raw := flagURL
	if raw == "" {
		raw = os.Getenv("WAVONYX_URL")
	}
	if raw == "" {
		if addr := os.Getenv("WAVONYX_ADDR"); addr != "" {
			if host, port, err := net.SplitHostPort(addr); err == nil {
				if host == "" || host == "0.0.0.0" || host == "::" {
					host = "127.0.0.1"
				}
				raw = "http://" + net.JoinHostPort(host, port)
			}
		}
	}
	if raw == "" {
		raw = "http://127.0.0.1:9900"
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	return strings.TrimRight(raw, "/")
}

func (c *client) request(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, c.unreachable(err)
	}
	return resp, nil
}

// unreachable turns a dial failure into advice instead of a raw network error.
func (c *client) unreachable(err error) error {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("cannot reach the Wavonyx server at %s: %v\n"+
			"Is it running? Start it with 'wavonyx serve', or point the CLI elsewhere with --url / WAVONYX_URL", c.baseURL, err)
	}
	return err
}

// do performs a request and decodes the JSON envelope into out (may be nil).
func (c *client) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	resp, err := c.request(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return decodeEnvelope(resp, out)
}

func (c *client) getJSON(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, "", out)
}

func (c *client) postJSON(ctx context.Context, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	return c.do(ctx, http.MethodPost, path, r, "application/json", out)
}

func (c *client) deleteJSON(ctx context.Context, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	return c.do(ctx, http.MethodDelete, path, r, "application/json", out)
}

// decodeEnvelope reads the {"data"|"error", "meta"} envelope, turning a non-2xx
// response into an *apiError.
func decodeEnvelope(resp *http.Response, out any) error {
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	_ = json.Unmarshal(raw, &env)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		e := &apiError{Status: resp.StatusCode}
		if env.Error != nil {
			e.Code, e.Message = env.Error.Code, env.Error.Message
		}
		return e
	}
	if out != nil && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

// --- API calls ---

func (c *client) listSessions(ctx context.Context) ([]wavonyx.SessionInfo, error) {
	var out struct {
		Sessions []wavonyx.SessionInfo `json:"sessions"`
	}
	if err := c.getJSON(ctx, "/sessions", &out); err != nil {
		return nil, err
	}
	return out.Sessions, nil
}

func (c *client) getSession(ctx context.Context, id string) (*wavonyx.SessionInfo, error) {
	var info wavonyx.SessionInfo
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(id), &info); err != nil {
		return nil, err
	}
	return &info, nil
}

func (c *client) createSession(ctx context.Context, id, webhookURL string) (*wavonyx.SessionInfo, error) {
	var info wavonyx.SessionInfo
	body := map[string]string{"id": id, "webhook_url": webhookURL}
	if err := c.postJSON(ctx, "/sessions", body, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// updateWebhook points a session at a new webhook URL; "" clears the override.
func (c *client) updateWebhook(ctx context.Context, id, webhookURL string) (*wavonyx.SessionInfo, error) {
	var info wavonyx.SessionInfo
	body := map[string]string{"webhook_url": webhookURL}
	if err := c.do(ctx, http.MethodPatch, "/sessions/"+url.PathEscape(id), jsonBody(body), "application/json", &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// jsonBody marshals v into a reader, ignoring errors that cannot occur for the
// simple maps used here.
func jsonBody(v any) io.Reader {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b)
}

func (c *client) login(ctx context.Context, id string) (*wavonyx.QRInfo, error) {
	var out struct {
		Status string          `json:"status"`
		QR     *wavonyx.QRInfo `json:"qr"`
	}
	if err := c.postJSON(ctx, "/sessions/"+url.PathEscape(id)+"/login", nil, &out); err != nil {
		return nil, err
	}
	return out.QR, nil
}

func (c *client) qr(ctx context.Context, id string) (*wavonyx.QRInfo, error) {
	var out struct {
		QR *wavonyx.QRInfo `json:"qr"`
	}
	if err := c.getJSON(ctx, "/sessions/"+url.PathEscape(id)+"/qr", &out); err != nil {
		return nil, err
	}
	return out.QR, nil
}

func (c *client) logout(ctx context.Context, id string) error {
	return c.postJSON(ctx, "/sessions/"+url.PathEscape(id)+"/logout", nil, nil)
}

func (c *client) deleteSession(ctx context.Context, id string) error {
	return c.deleteJSON(ctx, "/sessions/"+url.PathEscape(id), nil, nil)
}

func (c *client) sendText(ctx context.Context, id, to, text string) (*wavonyx.SendResult, error) {
	var res wavonyx.SendResult
	body := map[string]string{"to": to, "text": text}
	if err := c.postJSON(ctx, "/sessions/"+url.PathEscape(id)+"/messages", body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// sendMedia uploads a file as multipart/form-data, streaming it from disk.
func (c *client) sendMedia(ctx context.Context, id, to, caption, path string) (*wavonyx.SendResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("to", to); err != nil {
		return nil, err
	}
	if caption != "" {
		if err := mw.WriteField("caption", caption); err != nil {
			return nil, err
		}
	}
	part, err := mw.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	var res wavonyx.SendResult
	if err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(id)+"/messages", &buf, mw.FormDataContentType(), &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *client) messages(ctx context.Context, id string, limit int) ([]wavonyx.InboundMessage, error) {
	var out struct {
		Messages []wavonyx.InboundMessage `json:"messages"`
	}
	path := "/sessions/" + url.PathEscape(id) + "/messages?limit=" + strconv.Itoa(limit)
	if err := c.getJSON(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Messages, nil
}

func (c *client) editMessage(ctx context.Context, id, to, messageID, text string) (*wavonyx.SendResult, error) {
	var res wavonyx.SendResult
	body := map[string]string{"to": to, "text": text}
	path := "/sessions/" + url.PathEscape(id) + "/messages/" + url.PathEscape(messageID) + "/edit"
	if err := c.postJSON(ctx, path, body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (c *client) revokeMessage(ctx context.Context, id, to, messageID string) (*wavonyx.SendResult, error) {
	var res wavonyx.SendResult
	body := map[string]string{"to": to}
	path := "/sessions/" + url.PathEscape(id) + "/messages/" + url.PathEscape(messageID)
	if err := c.deleteJSON(ctx, path, body, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// downloadMedia fetches an inbound attachment, returning its bytes and the
// filename the server suggested (may be empty).
func (c *client) downloadMedia(ctx context.Context, id, token string) ([]byte, string, error) {
	path := "/sessions/" + url.PathEscape(id) + "/media?token=" + url.QueryEscape(token)
	resp, err := c.request(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", decodeEnvelope(resp, nil)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	name := ""
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil {
		name = params["filename"]
	}
	return data, name, nil
}
