package wavonyx

import (
	"errors"
	"testing"
)

func TestNormalizeRecipient(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain international", "6281234567890", "6281234567890@s.whatsapp.net", false},
		{"plus and separators", "+62 812-3456-7890", "6281234567890@s.whatsapp.net", false},
		{"parens and dots", "+1 (415) 555.1234", "14155551234@s.whatsapp.net", false},
		{"leading zero local", "081234567890", "", true},
		{"too short", "123456", "", true},
		{"too long", "1234567890123456", "", true},
		{"contains letters", "62abc456789", "", true},
		{"empty", "", "", true},
		{"whitespace only", "   ", "", true},
		{"explicit user jid", "6281234567890@s.whatsapp.net", "6281234567890@s.whatsapp.net", false},
		{"group jid", "120363012345678901@g.us", "120363012345678901@g.us", false},
		{"lid jid", "12345678901234@lid", "12345678901234@lid", false},
		{"unknown server", "user@example.com", "", true},
		{"jid internal space", "628 12@s.whatsapp.net", "", true},
		{"jid empty user", "@s.whatsapp.net", "", true},
		{"jid trailing space trimmed", " 6281234567890@s.whatsapp.net ", "6281234567890@s.whatsapp.net", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeRecipient(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				if !errors.Is(err, ErrInvalidRecipient) {
					t.Fatalf("want ErrInvalidRecipient, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}
