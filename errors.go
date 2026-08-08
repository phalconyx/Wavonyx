package wavonyx

import (
	"errors"
	"fmt"
)

// Sentinel errors returned by the session manager and related helpers. The HTTP
// server maps each to a stable error code and status; matching is done with
// errors.Is so wrapped errors still classify correctly.
var (
	// ErrInvalidSessionID means a session id failed the slug format check.
	ErrInvalidSessionID = errors.New("wavonyx: invalid session id")
	// ErrSessionNotFound means no session exists with the given id.
	ErrSessionNotFound = errors.New("wavonyx: session not found")
	// ErrSessionExists means a session with the requested id already exists.
	ErrSessionExists = errors.New("wavonyx: session already exists")
	// ErrAlreadyConnected means an operation requires a non-connected session.
	ErrAlreadyConnected = errors.New("wavonyx: session already connected")
	// ErrNotConnected means an operation requires a connected session.
	ErrNotConnected = errors.New("wavonyx: session not connected")
	// ErrNotLoggedIn means logout was requested on a never-paired session.
	ErrNotLoggedIn = errors.New("wavonyx: session not logged in")
	// ErrQRNotAvailable means no current pairing QR code is available.
	ErrQRNotAvailable = errors.New("wavonyx: qr code not available")
	// ErrQueueFull means the session's outbound send queue is at capacity.
	ErrQueueFull = errors.New("wavonyx: send queue full")
	// ErrInvalidRecipient means a recipient could not be parsed into a JID.
	ErrInvalidRecipient = errors.New("wavonyx: invalid recipient")
	// ErrEmptyText means an outgoing message had no text content.
	ErrEmptyText = errors.New("wavonyx: message text is empty")
	// ErrInvalidTyping means the request's typing options were out of range.
	ErrInvalidTyping = errors.New("wavonyx: invalid typing options")
	// ErrInvalidToken means a media download token could not be decoded.
	ErrInvalidToken = errors.New("wavonyx: invalid media token")
	// ErrMissingMedia means an outbound media send had no file data.
	ErrMissingMedia = errors.New("wavonyx: missing media data")
	// ErrMediaTooLarge means an outbound media file exceeded MaxMediaBytes.
	ErrMediaTooLarge = errors.New("wavonyx: media file too large")
	// ErrInvalidWebhook means a webhook URL was not a usable http(s) URL.
	ErrInvalidWebhook = errors.New("wavonyx: invalid webhook url")
)

// SendError wraps a failure to deliver an outgoing message to WhatsApp. It
// carries the (already-normalized) recipient for context and unwraps to the
// underlying cause so callers can inspect it with errors.As/Is.
type SendError struct {
	To  string
	Err error
}

func (e *SendError) Error() string {
	return fmt.Sprintf("wavonyx: send to %s failed: %v", e.To, e.Err)
}

func (e *SendError) Unwrap() error { return e.Err }

// MediaError wraps a failure to download inbound media.
type MediaError struct {
	Err error
}

func (e *MediaError) Error() string {
	return fmt.Sprintf("wavonyx: media download failed: %v", e.Err)
}

func (e *MediaError) Unwrap() error { return e.Err }
