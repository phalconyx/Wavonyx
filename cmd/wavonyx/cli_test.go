package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/phalconyx/wavonyx"
)

func TestResolveBaseURL(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "")
		t.Setenv("WAVONYX_ADDR", "")
		if got := resolveBaseURL(""); got != "http://127.0.0.1:9900" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("from listen addr", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "")
		t.Setenv("WAVONYX_ADDR", ":9901")
		if got := resolveBaseURL(""); got != "http://127.0.0.1:9901" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("wildcard host becomes loopback", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "")
		t.Setenv("WAVONYX_ADDR", "0.0.0.0:9900")
		if got := resolveBaseURL(""); got != "http://127.0.0.1:9900" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("url env wins over addr", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "https://wa.example.com/")
		t.Setenv("WAVONYX_ADDR", ":9901")
		if got := resolveBaseURL(""); got != "https://wa.example.com" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "https://env.example.com")
		if got := resolveBaseURL("http://flag.example.com"); got != "http://flag.example.com" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("bare host gets a scheme", func(t *testing.T) {
		t.Setenv("WAVONYX_URL", "")
		t.Setenv("WAVONYX_ADDR", "")
		if got := resolveBaseURL("wa.example.com:9900"); got != "http://wa.example.com:9900" {
			t.Fatalf("got %q", got)
		}
	})
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantCmd string
		wantRes []string
	}{
		{"plain", []string{"list"}, "list", nil},
		{"flags after command stay put", []string{"list", "--json"}, "list", []string{"--json"}},
		{"value flag before command moves", []string{"--url", "http://x", "list"}, "list", []string{"--url", "http://x"}},
		{"bool flag before command moves", []string{"--json", "list"}, "list", []string{"--json"}},
		{"inline value", []string{"--url=http://x", "list"}, "list", []string{"--url=http://x"}},
		{"single dash form", []string{"-key", "k1", "list"}, "list", []string{"-key", "k1"}},
		{"globals merge with command args", []string{"--json", "send", "s", "628", "hi"}, "send", []string{"--json", "s", "628", "hi"}},
		{"help shortcut untouched", []string{"-h"}, "-h", nil},
		{"no command", []string{"--json"}, "", []string{"--json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd, rest := splitCommand(tt.args)
			if cmd != tt.wantCmd {
				t.Fatalf("command = %q, want %q", cmd, tt.wantCmd)
			}
			if strings.Join(rest, " ") != strings.Join(tt.wantRes, " ") {
				t.Fatalf("rest = %v, want %v", rest, tt.wantRes)
			}
		})
	}
}

func TestPermuteFlags(t *testing.T) {
	newFS := func() *flag.FlagSet {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.Int("n", 20, "")
		fs.Bool("clear", false, "")
		fs.String("o", "", "")
		return fs
	}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"already ordered", []string{"-n", "5", "personal"}, []string{"-n", "5", "personal"}},
		{"trailing value flag", []string{"personal", "-n", "5"}, []string{"-n", "5", "personal"}},
		{"trailing bool flag", []string{"cs", "--clear"}, []string{"--clear", "cs"}},
		{"inline value", []string{"cs", "-n=5"}, []string{"-n=5", "cs"}},
		{"mixed", []string{"cs", "-o", "f.pdf", "tok"}, []string{"-o", "f.pdf", "cs", "tok"}},
		{"double dash ends flags", []string{"cs", "--", "-n", "5"}, []string{"cs", "-n", "5"}},
		{"no flags", []string{"cs", "tok"}, []string{"cs", "tok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := permuteFlags(newFS(), tt.args)
			if strings.Join(got, " ") != strings.Join(tt.want, " ") {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
}

// TestParseFlagsAnyOrderEndToEnd locks in the behaviour the permutation exists
// for: a flag written after the positional argument must still take effect.
func TestParseFlagsAnyOrderEndToEnd(t *testing.T) {
	fs := flag.NewFlagSet("messages", flag.ContinueOnError)
	limit := fs.Int("n", 20, "")
	parseFlagsAnyOrder(fs, []string{"personal", "-n", "5"}, "usage")
	if *limit != 5 {
		t.Fatalf("limit = %d, want 5", *limit)
	}
	if fs.Arg(0) != "personal" {
		t.Fatalf("positional = %q", fs.Arg(0))
	}

	fs2 := flag.NewFlagSet("webhook", flag.ContinueOnError)
	clear := fs2.Bool("clear", false, "")
	parseFlagsAnyOrder(fs2, []string{"cs", "--clear"}, "usage")
	if !*clear || fs2.Arg(0) != "cs" || fs2.Arg(1) != "" {
		t.Fatalf("clear=%v args=%v", *clear, fs2.Args())
	}
}

// newTestClient wires a client to a stub server.
func newTestClient(t *testing.T, h http.HandlerFunc) *client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return newClient(srv.URL, "testkey", 5*time.Second)
}

func writeData(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": map[string]any{"request_id": "req_test"}})
}

func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": errCode, "message": msg},
		"meta":  map[string]any{"request_id": "req_test"},
	})
}

func TestClientSendsAPIKeyAndParsesEnvelope(t *testing.T) {
	var gotKey, gotPath string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotKey, gotPath = r.Header.Get("X-API-Key"), r.URL.Path
		writeData(w, 200, map[string]any{"sessions": []wavonyx.SessionInfo{{ID: "personal", Status: wavonyx.StatusConnected, Phone: "628"}}})
	})

	sessions, err := c.listSessions(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if gotKey != "testkey" || gotPath != "/sessions" {
		t.Fatalf("request: key=%q path=%q", gotKey, gotPath)
	}
	if len(sessions) != 1 || sessions[0].ID != "personal" || sessions[0].Status != wavonyx.StatusConnected {
		t.Fatalf("sessions: %+v", sessions)
	}
}

func TestClientErrorEnvelope(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusConflict, "not_connected", "wavonyx: session not connected")
	})

	_, err := c.sendText(context.Background(), "personal", "628123", "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if !codeIs(err, "not_connected") {
		t.Fatalf("codeIs failed for %v", err)
	}
	if !strings.Contains(err.Error(), "not_connected") {
		t.Fatalf("message should carry the code: %v", err)
	}
}

func TestClientUnreachableGivesAdvice(t *testing.T) {
	// Port 1 on loopback refuses connections.
	c := newClient("http://127.0.0.1:1", "", 2*time.Second)
	_, err := c.listSessions(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "wavonyx serve") {
		t.Fatalf("error should suggest starting the server: %v", err)
	}
}

func TestClientSendMediaMultipart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.jpg")
	if err := os.WriteFile(path, []byte("\xff\xd8\xffJPEGDATA"), 0o600); err != nil {
		t.Fatal(err)
	}

	var gotTo, gotCaption, gotFilename string
	var gotData []byte
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("parse multipart: %v", err)
			writeErr(w, 400, "invalid_multipart", "bad")
			return
		}
		gotTo, gotCaption = r.FormValue("to"), r.FormValue("caption")
		f, header, err := r.FormFile("file")
		if err != nil {
			t.Errorf("form file: %v", err)
			writeErr(w, 400, "missing_file", "bad")
			return
		}
		defer f.Close()
		gotFilename = header.Filename
		gotData, _ = io.ReadAll(f)
		writeData(w, 201, wavonyx.SendResult{MessageID: "M1", To: "628123@s.whatsapp.net"})
	})

	res, err := c.sendMedia(context.Background(), "personal", "628123", "look", path)
	if err != nil {
		t.Fatalf("send media: %v", err)
	}
	if res.MessageID != "M1" {
		t.Fatalf("result: %+v", res)
	}
	if gotTo != "628123" || gotCaption != "look" || gotFilename != "photo.jpg" {
		t.Fatalf("fields: to=%q caption=%q file=%q", gotTo, gotCaption, gotFilename)
	}
	if string(gotData) != "\xff\xd8\xffJPEGDATA" {
		t.Fatalf("data: %q", gotData)
	}
}

func TestClientDownloadMedia(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("token"); got != "tok123" {
			t.Errorf("token = %q", got)
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="invoice.pdf"`)
		_, _ = w.Write([]byte("%PDF-1.4"))
	})

	data, name, err := c.downloadMedia(context.Background(), "personal", "tok123")
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(data) != "%PDF-1.4" || name != "invoice.pdf" {
		t.Fatalf("data=%q name=%q", data, name)
	}
}

func TestClientDownloadMediaError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusBadRequest, "invalid_token", "wavonyx: invalid media token")
	})
	if _, _, err := c.downloadMedia(context.Background(), "personal", "bad"); !codeIs(err, "invalid_token") {
		t.Fatalf("want invalid_token, got %v", err)
	}
}

func TestClientRevokeSendsChatInBody(t *testing.T) {
	var body map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s", r.Method)
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeData(w, 200, wavonyx.SendResult{MessageID: "R1", To: "628123@s.whatsapp.net"})
	})
	if _, err := c.revokeMessage(context.Background(), "personal", "628123", "MID"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if body["to"] != "628123" {
		t.Fatalf("body: %v", body)
	}
}

func TestClientUpdateWebhook(t *testing.T) {
	var gotMethod string
	var body map[string]string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeData(w, 200, wavonyx.SessionInfo{ID: "toko", WebhookURL: body["webhook_url"]})
	})

	info, err := c.updateWebhook(context.Background(), "toko", "https://app.example/wh")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if gotMethod != http.MethodPatch {
		t.Fatalf("method = %s, want PATCH", gotMethod)
	}
	if body["webhook_url"] != "https://app.example/wh" || info.WebhookURL != "https://app.example/wh" {
		t.Fatalf("body=%v info=%+v", body, info)
	}

	// Clearing sends an explicit empty string, not an omitted field.
	if _, err := c.updateWebhook(context.Background(), "toko", ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if v, ok := body["webhook_url"]; !ok || v != "" {
		t.Fatalf("clear body = %v", body)
	}
}

// --- QR rendering ---

// waCode is a stand-in for a real WhatsApp pairing code (~200 chars).
var waCode = "2@" + strings.Repeat("A1b2C3d4E5f6G7h8", 4) + "==," +
	strings.Repeat("xYz9", 11) + "=," + strings.Repeat("Qw3r", 11) + "=," + strings.Repeat("Mn7p", 11) + "="

func TestRenderQRFitsTerminal(t *testing.T) {
	art, err := renderQR(waCode, true, false)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(art, "\n"), "\n")
	if len(lines) > 40 {
		t.Fatalf("QR too tall for a terminal: %d rows", len(lines))
	}
	// Each cell is one half-block rune; ANSI escapes don't take screen columns.
	width := strings.Count(lines[0], "▀")
	if width == 0 || width > 80 {
		t.Fatalf("QR width %d does not fit an 80-column terminal", width)
	}
	for i, ln := range lines {
		if got := strings.Count(ln, "▀"); got != width {
			t.Fatalf("line %d has %d cells, want %d (rows must be rectangular)", i, got, width)
		}
	}
}

func TestRenderQRDeterministicAndModes(t *testing.T) {
	a, err := renderQR(waCode, true, false)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := renderQR(waCode, true, false)
	if a != b {
		t.Fatal("same input should render identically")
	}
	if inv, _ := renderQR(waCode, true, true); inv == a {
		t.Fatal("inverted rendering should differ")
	}

	plain, err := renderQR(waCode, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "\033[") {
		t.Fatal("no-colour mode must not emit ANSI escapes")
	}
	if !strings.Contains(a, "\033[") {
		t.Fatal("colour mode should emit ANSI escapes")
	}
}

func TestRenderQRRejectsOversizedContent(t *testing.T) {
	if _, err := renderQR(strings.Repeat("x", 8000), true, false); err == nil {
		t.Fatal("want error for content that cannot fit in a QR code")
	}
}

// --- live screen ---

func TestLiveScreenNonTTYJustAppends(t *testing.T) {
	var buf bytes.Buffer
	s := newLiveScreen(&buf, false)
	s.render("one\ntwo")
	s.render("three")
	out := buf.String()
	if strings.Contains(out, "\033[") {
		t.Fatalf("non-tty output must be escape-free: %q", out)
	}
	if !strings.Contains(out, "one") || !strings.Contains(out, "three") {
		t.Fatalf("output: %q", out)
	}
}

func TestLiveScreenTTYRedrawsInPlace(t *testing.T) {
	var buf bytes.Buffer
	s := newLiveScreen(&buf, true)
	s.render("a\nb\nc")
	first := buf.String()
	if strings.Contains(first, "\033[3A") {
		t.Fatal("first render should not move the cursor up")
	}
	buf.Reset()
	s.render("x")
	second := buf.String()
	if !strings.HasPrefix(second, "\033[3A") {
		t.Fatalf("redraw should rewind 3 lines: %q", second)
	}
	// The two stale lines must be cleared, not left on screen.
	if strings.Count(second, "\033[K") != 3 {
		t.Fatalf("expected 3 cleared lines: %q", second)
	}
}

// --- formatting ---

func TestMessageSummary(t *testing.T) {
	tests := []struct {
		name string
		msg  wavonyx.InboundMessage
		want string
	}{
		{"text", wavonyx.InboundMessage{Kind: wavonyx.KindText, Text: "hello"}, "hello"},
		{"newlines flattened", wavonyx.InboundMessage{Kind: wavonyx.KindText, Text: "a\nb"}, "a b"},
		{"edit marked", wavonyx.InboundMessage{Kind: wavonyx.KindText, Text: "fixed", EditedID: "M1"}, "(edited) fixed"},
		{"image no caption", wavonyx.InboundMessage{Kind: wavonyx.KindImage, Media: &wavonyx.MediaInfo{}}, "[image]"},
		{"image with caption", wavonyx.InboundMessage{Kind: wavonyx.KindImage, Text: "look", Media: &wavonyx.MediaInfo{}}, "[image] look"},
		{"document names the file", wavonyx.InboundMessage{Kind: wavonyx.KindDocument, Media: &wavonyx.MediaInfo{Filename: "a.pdf"}}, "[document a.pdf]"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageSummary(tt.msg); got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestSenderLabel(t *testing.T) {
	tests := []struct {
		msg  wavonyx.InboundMessage
		want string
	}{
		{wavonyx.InboundMessage{PushName: "Phalconyx", SenderPhone: "628"}, "Phalconyx (628)"},
		{wavonyx.InboundMessage{PushName: "Phalconyx"}, "Phalconyx"},
		{wavonyx.InboundMessage{SenderPhone: "628"}, "628"},
		{wavonyx.InboundMessage{Sender: "111@lid"}, "111@lid"},
	}
	for _, tt := range tests {
		if got := senderLabel(tt.msg); got != tt.want {
			t.Fatalf("got %q want %q", got, tt.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Fatalf("got %q", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Fatalf("got %q", got)
	}
	// Truncation counts runes, not bytes.
	if got := truncate("héllo wörld", 8); len([]rune(got)) != 8 {
		t.Fatalf("got %q (%d runes)", got, len([]rune(got)))
	}
}
