package wavonyx

import (
	"encoding/base64"
	"encoding/json"
)

// mediaRef is the portable download reference encoded into MediaInfo.Token. It
// carries everything whatsmeow needs to fetch and decrypt an attachment without
// Wavonyx persisting the original message — the same self-describing-link idea
// used by the sibling projects (telconyx/dragonyx).
type mediaRef struct {
	Kind       string `json:"k"`
	URL        string `json:"u,omitempty"`
	DirectPath string `json:"p"`
	MediaKey   []byte `json:"mk"`
	EncSHA256  []byte `json:"e"`
	SHA256     []byte `json:"s"`
	FileLength uint64 `json:"l,omitempty"`
	Mimetype   string `json:"m,omitempty"`
	Filename   string `json:"fn,omitempty"`
}

// encodeMediaRef serializes r to a compact URL-safe token.
func encodeMediaRef(r mediaRef) string {
	b, _ := json.Marshal(r)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeMediaRef parses a token produced by encodeMediaRef, returning
// ErrInvalidToken for anything malformed or missing the fields needed to
// download.
func decodeMediaRef(token string) (mediaRef, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return mediaRef{}, ErrInvalidToken
	}
	var r mediaRef
	if err := json.Unmarshal(raw, &r); err != nil {
		return mediaRef{}, ErrInvalidToken
	}
	if r.Kind == "" || r.DirectPath == "" || len(r.MediaKey) == 0 {
		return mediaRef{}, ErrInvalidToken
	}
	return r, nil
}
