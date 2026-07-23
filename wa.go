package wavonyx

import (
	"context"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
)

// qrItem is a wavonyx-owned pairing event, decoupled from whatsmeow's
// QRChannelItem so the session logic can be driven by an in-memory fake.
type qrItem struct {
	Event   string // "code", "success", "timeout", or "error"
	Code    string
	Timeout time.Duration
	Err     error
}

const (
	qrEventCode    = "code"
	qrEventSuccess = "success"
	qrEventTimeout = "timeout"
)

// waSendResult is the outcome of a successful send.
type waSendResult struct {
	ID        string
	Timestamp time.Time
}

// outboundMedia is an attachment to upload and send.
type outboundMedia struct {
	Data     []byte
	Mimetype string
	Kind     string // KindImage/KindVideo/KindAudio/KindVoice/KindDocument
	Caption  string
	Filename string
}

// waHandlers are the callbacks a session registers on its client. Any field may
// be nil.
type waHandlers struct {
	OnMessage      func(eventType string, m InboundMessage)
	OnConnected    func()
	OnDisconnected func()
	OnLoggedOut    func()
}

// waClient abstracts a single whatsmeow client. The session layer depends only
// on this interface, so it can be exercised with an in-memory fake in tests;
// realClient is the whatsmeow-backed implementation.
type waClient interface {
	// SetHandlers registers lifecycle/message callbacks. Call before Connect.
	SetHandlers(h waHandlers)
	// PairQR starts a pairing session and streams QR events. It must be called
	// before Connect and only when the client is not yet paired.
	PairQR(ctx context.Context) (<-chan qrItem, error)
	Connect() error
	Disconnect()
	Logout(ctx context.Context) error // unlink device and remove it from the store
	IsConnected() bool
	IsLoggedIn() bool
	JID() string      // "" until paired
	PushName() string // "" until known
	SendText(ctx context.Context, jid, text string) (waSendResult, error)
	SendMedia(ctx context.Context, jid string, m outboundMedia) (waSendResult, error)
	EditText(ctx context.Context, jid, messageID, newText string) (waSendResult, error)
	RevokeMessage(ctx context.Context, jid, messageID string) (waSendResult, error)
	SendAvailable(ctx context.Context) error                     // presence "available"
	SetComposing(ctx context.Context, jid string, on bool) error // typing indicator
	// MarkRead sends a read receipt (blue ticks) for the given inbound message
	// ids. chat is the chat JID; sender is the author (required for groups).
	MarkRead(ctx context.Context, ids []string, ts time.Time, chat, sender string) error
	// DownloadMedia fetches and decrypts the attachment referenced by ref.
	DownloadMedia(ctx context.Context, ref mediaRef) ([]byte, error)
}

// waStore abstracts the whatsmeow device store (an sqlstore.Container).
type waStore interface {
	NewClient() (waClient, error)                                 // fresh, unpaired device
	LoadClient(ctx context.Context, jid string) (waClient, error) // existing device by JID
	DeleteDevice(ctx context.Context, jid string) error
	LoggedInJIDs(ctx context.Context) ([]string, error)
	Close() error
}

var (
	_ waClient = (*realClient)(nil)
	_ waStore  = (*realStore)(nil)
)

// realStore adapts *sqlstore.Container to waStore.
type realStore struct {
	container *sqlstore.Container
	log       waLog.Logger
}

func newRealStore(container *sqlstore.Container, log waLog.Logger) *realStore {
	if log == nil {
		log = waLog.Noop
	}
	return &realStore{container: container, log: log}
}

func (s *realStore) NewClient() (waClient, error) {
	dev := s.container.NewDevice()
	return newRealClient(whatsmeow.NewClient(dev, s.log)), nil
}

func (s *realStore) LoadClient(ctx context.Context, jid string) (waClient, error) {
	j, err := types.ParseJID(jid)
	if err != nil {
		return nil, err
	}
	dev, err := s.container.GetDevice(ctx, j)
	if err != nil {
		return nil, err
	}
	if dev == nil {
		return nil, ErrSessionNotFound
	}
	return newRealClient(whatsmeow.NewClient(dev, s.log)), nil
}

func (s *realStore) DeleteDevice(ctx context.Context, jid string) error {
	j, err := types.ParseJID(jid)
	if err != nil {
		return err
	}
	dev, err := s.container.GetDevice(ctx, j)
	if err != nil {
		return err
	}
	if dev == nil {
		return nil // already gone
	}
	return s.container.DeleteDevice(ctx, dev)
}

func (s *realStore) LoggedInJIDs(ctx context.Context) ([]string, error) {
	devs, err := s.container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(devs))
	for _, d := range devs {
		if d.ID != nil {
			out = append(out, d.ID.String())
		}
	}
	return out, nil
}

func (s *realStore) Close() error { return s.container.Close() }

// realClient adapts *whatsmeow.Client to waClient.
type realClient struct {
	cli *whatsmeow.Client
}

func newRealClient(cli *whatsmeow.Client) *realClient {
	cli.EnableAutoReconnect = true
	return &realClient{cli: cli}
}

func (c *realClient) SetHandlers(h waHandlers) {
	c.cli.AddEventHandler(func(evt any) {
		switch e := evt.(type) {
		case *events.Message:
			if h.OnMessage != nil {
				if m, evType, ok := parseMessage(e); ok {
					h.OnMessage(evType, m)
				}
			}
		case *events.Connected:
			if h.OnConnected != nil {
				h.OnConnected()
			}
		case *events.Disconnected:
			if h.OnDisconnected != nil {
				h.OnDisconnected()
			}
		case *events.StreamReplaced:
			if h.OnDisconnected != nil {
				h.OnDisconnected()
			}
		case *events.LoggedOut:
			if h.OnLoggedOut != nil {
				h.OnLoggedOut()
			}
		}
	})
}

func (c *realClient) PairQR(ctx context.Context) (<-chan qrItem, error) {
	ch, err := c.cli.GetQRChannel(ctx)
	if err != nil {
		return nil, err
	}
	out := make(chan qrItem, 8)
	go func() {
		defer close(out)
		for it := range ch {
			out <- qrItem{Event: it.Event, Code: it.Code, Timeout: it.Timeout, Err: it.Error}
		}
	}()
	return out, nil
}

func (c *realClient) Connect() error                   { return c.cli.Connect() }
func (c *realClient) Disconnect()                      { c.cli.Disconnect() }
func (c *realClient) Logout(ctx context.Context) error { return c.cli.Logout(ctx) }
func (c *realClient) IsConnected() bool                { return c.cli.IsConnected() }
func (c *realClient) IsLoggedIn() bool                 { return c.cli.IsLoggedIn() }

func (c *realClient) JID() string {
	if c.cli.Store == nil || c.cli.Store.ID == nil {
		return ""
	}
	return c.cli.Store.ID.String()
}

func (c *realClient) PushName() string {
	if c.cli.Store == nil {
		return ""
	}
	return c.cli.Store.PushName
}

func (c *realClient) SendText(ctx context.Context, jid, text string) (waSendResult, error) {
	j, err := types.ParseJID(jid)
	if err != nil {
		return waSendResult{}, err
	}
	resp, err := c.cli.SendMessage(ctx, j, &waE2E.Message{Conversation: proto.String(text)})
	if err != nil {
		return waSendResult{}, err
	}
	return waSendResult{ID: resp.ID, Timestamp: resp.Timestamp}, nil
}

func (c *realClient) SendMedia(ctx context.Context, jid string, m outboundMedia) (waSendResult, error) {
	j, err := types.ParseJID(jid)
	if err != nil {
		return waSendResult{}, err
	}
	up, err := c.cli.Upload(ctx, m.Data, mediaTypeFor(m.Kind))
	if err != nil {
		return waSendResult{}, err
	}
	resp, err := c.cli.SendMessage(ctx, j, buildMediaMessage(m, up))
	if err != nil {
		return waSendResult{}, err
	}
	return waSendResult{ID: resp.ID, Timestamp: resp.Timestamp}, nil
}

func mediaTypeFor(kind string) whatsmeow.MediaType {
	switch kind {
	case KindVideo:
		return whatsmeow.MediaVideo
	case KindAudio, KindVoice:
		return whatsmeow.MediaAudio
	case KindDocument:
		return whatsmeow.MediaDocument
	default: // image / sticker
		return whatsmeow.MediaImage
	}
}

// buildMediaMessage assembles the protobuf message for an uploaded attachment.
func buildMediaMessage(m outboundMedia, up whatsmeow.UploadResponse) *waE2E.Message {
	switch m.Kind {
	case KindVideo:
		return &waE2E.Message{VideoMessage: &waE2E.VideoMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), Mimetype: proto.String(m.Mimetype),
			Caption: strOrNil(m.Caption),
		}}
	case KindAudio, KindVoice:
		am := &waE2E.AudioMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), Mimetype: proto.String(m.Mimetype),
		}
		if m.Kind == KindVoice {
			am.PTT = proto.Bool(true)
		}
		return &waE2E.Message{AudioMessage: am}
	case KindDocument:
		return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), Mimetype: proto.String(m.Mimetype),
			FileName: strOrNil(m.Filename), Caption: strOrNil(m.Caption),
		}}
	default: // image
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath),
			MediaKey: up.MediaKey, FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength), Mimetype: proto.String(m.Mimetype),
			Caption: strOrNil(m.Caption),
		}}
	}
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (c *realClient) EditText(ctx context.Context, jid, messageID, newText string) (waSendResult, error) {
	j, err := types.ParseJID(jid)
	if err != nil {
		return waSendResult{}, err
	}
	edited := c.cli.BuildEdit(j, messageID, &waE2E.Message{Conversation: proto.String(newText)})
	resp, err := c.cli.SendMessage(ctx, j, edited)
	if err != nil {
		return waSendResult{}, err
	}
	return waSendResult{ID: resp.ID, Timestamp: resp.Timestamp}, nil
}

func (c *realClient) RevokeMessage(ctx context.Context, jid, messageID string) (waSendResult, error) {
	j, err := types.ParseJID(jid)
	if err != nil {
		return waSendResult{}, err
	}
	resp, err := c.cli.SendMessage(ctx, j, c.cli.BuildRevoke(j, types.EmptyJID, messageID))
	if err != nil {
		return waSendResult{}, err
	}
	return waSendResult{ID: resp.ID, Timestamp: resp.Timestamp}, nil
}

func (c *realClient) SendAvailable(ctx context.Context) error {
	return c.cli.SendPresence(ctx, types.PresenceAvailable)
}

func (c *realClient) SetComposing(ctx context.Context, jid string, on bool) error {
	j, err := types.ParseJID(jid)
	if err != nil {
		return err
	}
	state := types.ChatPresencePaused
	if on {
		state = types.ChatPresenceComposing
	}
	return c.cli.SendChatPresence(ctx, j, state, types.ChatPresenceMediaText)
}

func (c *realClient) MarkRead(ctx context.Context, ids []string, ts time.Time, chat, sender string) error {
	if len(ids) == 0 {
		return nil
	}
	chatJID, err := types.ParseJID(chat)
	if err != nil {
		return err
	}
	var senderJID types.JID
	if sender != "" {
		if senderJID, err = types.ParseJID(sender); err != nil {
			return err
		}
	}
	return c.cli.MarkRead(ctx, ids, ts, chatJID, senderJID)
}

func (c *realClient) DownloadMedia(ctx context.Context, ref mediaRef) ([]byte, error) {
	dl := reconstructDownloadable(ref)
	if dl == nil {
		return nil, fmt.Errorf("unsupported media kind %q", ref.Kind)
	}
	return c.cli.Download(ctx, dl)
}

// reconstructDownloadable rebuilds the minimal protobuf message whatsmeow needs
// to download an attachment from a mediaRef. The concrete type tells whatsmeow
// which media endpoint and encryption keys to use.
func reconstructDownloadable(ref mediaRef) whatsmeow.DownloadableMessage {
	switch ref.Kind {
	case KindImage:
		return &waE2E.ImageMessage{URL: proto.String(ref.URL), DirectPath: proto.String(ref.DirectPath), MediaKey: ref.MediaKey, FileEncSHA256: ref.EncSHA256, FileSHA256: ref.SHA256, FileLength: proto.Uint64(ref.FileLength), Mimetype: proto.String(ref.Mimetype)}
	case KindVideo:
		return &waE2E.VideoMessage{URL: proto.String(ref.URL), DirectPath: proto.String(ref.DirectPath), MediaKey: ref.MediaKey, FileEncSHA256: ref.EncSHA256, FileSHA256: ref.SHA256, FileLength: proto.Uint64(ref.FileLength), Mimetype: proto.String(ref.Mimetype)}
	case KindAudio, KindVoice:
		return &waE2E.AudioMessage{URL: proto.String(ref.URL), DirectPath: proto.String(ref.DirectPath), MediaKey: ref.MediaKey, FileEncSHA256: ref.EncSHA256, FileSHA256: ref.SHA256, FileLength: proto.Uint64(ref.FileLength), Mimetype: proto.String(ref.Mimetype)}
	case KindDocument:
		return &waE2E.DocumentMessage{URL: proto.String(ref.URL), DirectPath: proto.String(ref.DirectPath), MediaKey: ref.MediaKey, FileEncSHA256: ref.EncSHA256, FileSHA256: ref.SHA256, FileLength: proto.Uint64(ref.FileLength), Mimetype: proto.String(ref.Mimetype)}
	case KindSticker:
		return &waE2E.StickerMessage{URL: proto.String(ref.URL), DirectPath: proto.String(ref.DirectPath), MediaKey: ref.MediaKey, FileEncSHA256: ref.EncSHA256, FileSHA256: ref.SHA256, FileLength: proto.Uint64(ref.FileLength), Mimetype: proto.String(ref.Mimetype)}
	}
	return nil
}
