package wavonyx

import (
	"strings"
	"time"

	"github.com/phalconyx/wavonyx/typing"
)

// Status is the lifecycle state of a session.
type Status string

const (
	// StatusCreated: the session exists but has never been paired.
	StatusCreated Status = "created"
	// StatusPairing: a QR code is being offered and awaiting a scan.
	StatusPairing Status = "pairing"
	// StatusConnected: paired and connected to WhatsApp.
	StatusConnected Status = "connected"
	// StatusDisconnected: paired but not currently connected (whatsmeow will
	// usually auto-reconnect).
	StatusDisconnected Status = "disconnected"
	// StatusLoggedOut: the device was unlinked; re-pairing is required.
	StatusLoggedOut Status = "logged_out"
)

// Message kinds carried in InboundMessage.Kind. Media captions go in Text.
const (
	KindText     = "text"
	KindImage    = "image"
	KindVideo    = "video"
	KindAudio    = "audio"
	KindVoice    = "voice" // push-to-talk voice note
	KindDocument = "document"
	KindSticker  = "sticker"
)

// Webhook event types.
const (
	EventMessage = "message"        // a new inbound message
	EventEdit    = "message.edit"   // a previous message's content changed
	EventRevoke  = "message.revoke" // a previous message was deleted for everyone
)

// InboundMessage is a parsed incoming WhatsApp message. It is also the payload
// carried by a webhook "message" event and returned by the ring-buffer
// endpoint.
type InboundMessage struct {
	MessageID   string     `json:"message_id"`
	Chat        string     `json:"chat"`         // JID: "...@s.whatsapp.net" or "...@g.us"
	Sender      string     `json:"sender"`       // participant JID (differs from Chat in groups)
	SenderPhone string     `json:"sender_phone"` // digits only; "" if unresolvable
	PushName    string     `json:"push_name"`
	IsGroup     bool       `json:"is_group"`
	IsFromMe    bool       `json:"is_from_me"`
	Timestamp   time.Time  `json:"timestamp"`
	Kind        string     `json:"kind"`
	Text        string     `json:"text"`
	EditedID    string     `json:"edited_id,omitempty"` // message.edit: the original message being changed
	Media       *MediaInfo `json:"media,omitempty"`
	Quoted      *Quoted    `json:"quoted,omitempty"`
}

// Quoted is the message an inbound message replied to, when present.
type Quoted struct {
	MessageID string `json:"message_id"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
}

// MediaInfo describes an inbound media attachment. Token is an opaque reference
// passed to GET /sessions/{id}/media to download the actual bytes; it is only
// valid for a limited time (WhatsApp's media CDN paths expire).
type MediaInfo struct {
	Mimetype string `json:"mimetype,omitempty"`
	FileSize uint64 `json:"file_size,omitempty"`
	Filename string `json:"filename,omitempty"` // documents
	Width    int    `json:"width,omitempty"`    // image/video/sticker
	Height   int    `json:"height,omitempty"`   // image/video/sticker
	Duration int    `json:"duration,omitempty"` // seconds, audio/video
	Token    string `json:"token"`
}

// MediaContent is downloaded media, ready to be served.
type MediaContent struct {
	Data     []byte
	Mimetype string
	Filename string
}

// SendRequest is the body of POST /sessions/{id}/messages. Typing is optional;
// when nil the session's default typing configuration applies.
type SendRequest struct {
	To     string           `json:"to"`
	Text   string           `json:"text"`
	Typing *typing.Override `json:"typing,omitempty"`
}

// Validate checks the request's own fields. It does not resolve the recipient
// (done separately via NormalizeRecipient) so it stays a cheap, pure check.
func (r SendRequest) Validate() error {
	if strings.TrimSpace(r.Text) == "" {
		return ErrEmptyText
	}
	return validateTypingOverride(r.Typing)
}

// validateTypingOverride rejects out-of-range or unknown typing options.
func validateTypingOverride(o *typing.Override) error {
	if o == nil {
		return nil
	}
	if o.Mode != nil {
		switch *o.Mode {
		case typing.ModeOff, typing.ModeConstant, typing.ModeNatural:
		default:
			return ErrInvalidTyping
		}
	}
	if o.PerCharMS != nil && *o.PerCharMS < 0 {
		return ErrInvalidTyping
	}
	if o.MaxTotalMS != nil && *o.MaxTotalMS < 0 {
		return ErrInvalidTyping
	}
	if o.MinCPS != nil && *o.MinCPS < 0 {
		return ErrInvalidTyping
	}
	if o.MaxCPS != nil && *o.MaxCPS < 0 {
		return ErrInvalidTyping
	}
	return nil
}

// SendResult is returned once a message has been sent. TypingMS is the typing
// delay actually applied; QueuedMS is how long the request waited behind other
// sends on the same session.
type SendResult struct {
	MessageID string    `json:"message_id"`
	To        string    `json:"to"`
	Timestamp time.Time `json:"timestamp"`
	TypingMS  int64     `json:"typing_ms"`
	QueuedMS  int64     `json:"queued_ms"`
}

// MediaSendRequest carries an outbound attachment. The HTTP server builds it
// from a multipart request; Data holds the raw file bytes. Kind is derived from
// Mimetype (see mediaKindFromMimetype).
type MediaSendRequest struct {
	To       string
	Caption  string
	Typing   *typing.Override
	Data     []byte
	Mimetype string
	Filename string
}

// mediaKindFromMimetype maps a MIME type to a Wavonyx media kind. Anything not
// recognized as image/video/audio is sent as a document.
func mediaKindFromMimetype(mt string) string {
	switch {
	case strings.HasPrefix(mt, "image/"):
		return KindImage
	case strings.HasPrefix(mt, "video/"):
		return KindVideo
	case strings.HasPrefix(mt, "audio/"):
		return KindAudio
	default:
		return KindDocument
	}
}

// SessionInfo is the public view of a session.
type SessionInfo struct {
	ID         string    `json:"id"`
	Status     Status    `json:"status"`
	JID        string    `json:"jid"`
	Phone      string    `json:"phone"`
	PushName   string    `json:"push_name"`
	WebhookURL string    `json:"webhook_url"`
	CreatedAt  time.Time `json:"created_at"`
}

// QRInfo is a pairing QR code and its expiry. ExpiresIn is the whole seconds
// remaining until ExpiresAt at the moment the value was produced.
type QRInfo struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	ExpiresIn int       `json:"expires_in"`
}

// Event is the JSON body delivered to a webhook.
type Event struct {
	Event     string         `json:"event"`
	SessionID string         `json:"session_id"`
	TS        time.Time      `json:"ts"`
	Data      InboundMessage `json:"data"`
}
