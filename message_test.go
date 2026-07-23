package wavonyx

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/phalconyx/wavonyx/typing"
)

func TestSendRequestValidate(t *testing.T) {
	off := typing.ModeOff
	bad := typing.Mode("weird")
	neg := -5

	tests := []struct {
		name    string
		req     SendRequest
		wantErr error
	}{
		{"ok text", SendRequest{Text: "hello"}, nil},
		{"ok with typing", SendRequest{Text: "hi", Typing: &typing.Override{Mode: &off}}, nil},
		{"empty text", SendRequest{Text: ""}, ErrEmptyText},
		{"whitespace text", SendRequest{Text: "   "}, ErrEmptyText},
		{"bad mode", SendRequest{Text: "hi", Typing: &typing.Override{Mode: &bad}}, ErrInvalidTyping},
		{"negative per_char", SendRequest{Text: "hi", Typing: &typing.Override{PerCharMS: &neg}}, ErrInvalidTyping},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v want %v", err, tt.wantErr)
			}
		})
	}
}

// TestSendRequestJSONDecode locks in the JSON field names, including the
// typing.Override tags, so the HTTP body contract stays stable.
func TestSendRequestJSONDecode(t *testing.T) {
	body := `{"to":"628123","text":"halo","typing":{"mode":"natural","per_char_ms":40,"min_cps":5,"max_cps":8,"max_total_ms":8000}}`
	var r SendRequest
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if r.To != "628123" || r.Text != "halo" {
		t.Fatalf("scalar fields: %+v", r)
	}
	if r.Typing == nil {
		t.Fatal("typing not decoded")
	}
	if r.Typing.Mode == nil || *r.Typing.Mode != typing.ModeNatural {
		t.Fatalf("typing mode: %+v", r.Typing.Mode)
	}
	if r.Typing.PerCharMS == nil || *r.Typing.PerCharMS != 40 {
		t.Fatal("per_char_ms not decoded")
	}
	if r.Typing.MaxTotalMS == nil || *r.Typing.MaxTotalMS != 8000 {
		t.Fatal("max_total_ms not decoded")
	}
	if err := r.Validate(); err != nil {
		t.Fatalf("decoded request should validate: %v", err)
	}
}
