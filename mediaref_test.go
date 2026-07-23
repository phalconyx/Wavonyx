package wavonyx

import (
	"errors"
	"testing"
)

func TestMediaRefRoundTrip(t *testing.T) {
	orig := mediaRef{
		Kind: KindDocument, URL: "https://cdn/x", DirectPath: "/v/d.enc",
		MediaKey: []byte{1, 2, 3}, EncSHA256: []byte{4, 5}, SHA256: []byte{6, 7},
		FileLength: 999, Mimetype: "application/pdf", Filename: "a.pdf",
	}
	got, err := decodeMediaRef(encodeMediaRef(orig))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Kind != orig.Kind || got.URL != orig.URL || got.DirectPath != orig.DirectPath ||
		string(got.MediaKey) != string(orig.MediaKey) || string(got.EncSHA256) != string(orig.EncSHA256) ||
		string(got.SHA256) != string(orig.SHA256) || got.FileLength != orig.FileLength ||
		got.Mimetype != orig.Mimetype || got.Filename != orig.Filename {
		t.Fatalf("round-trip mismatch:\n got  %+v\n want %+v", got, orig)
	}
}

func TestDecodeMediaRefInvalid(t *testing.T) {
	cases := []string{
		"not!base64", // invalid base64 alphabet
		"",           // empty
		encodeMediaRef(mediaRef{Kind: KindImage}), // missing direct path + key
	}
	for _, tok := range cases {
		if _, err := decodeMediaRef(tok); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("token %q: want ErrInvalidToken, got %v", tok, err)
		}
	}
}
