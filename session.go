package wavonyx

import (
	"context"
	"log/slog"
	rand "math/rand/v2"
	"strings"
	"sync"
	"time"

	"github.com/phalconyx/wavonyx/typing"
)

// registryWriter is the subset of the registry a session needs.
type registryWriter interface {
	UpdateJID(ctx context.Context, id, jid string) error
}

// eventSink is the subset of the webhook dispatcher a session needs.
type eventSink interface {
	Enqueue(url string, ev Event)
}

// session is one WhatsApp account: its connection state, pairing/QR state, an
// inbound ring buffer, and a single-goroutine send queue that serializes and
// paces outgoing messages (the anti-ban queue). It is driven entirely through
// the waClient/waStore interfaces so it can be tested with in-memory fakes.
type session struct {
	id        string
	createdAt time.Time

	mu          sync.Mutex
	status      Status
	jid         string
	pushName    string
	webhookURL  string
	client      waClient
	qrCode      string
	qrExpiresAt time.Time
	pairCancel  context.CancelFunc
	unread      map[unreadKey]readTarget // latest unread inbound per chat+sender

	ring     *Ring
	sendCh   chan *sendJob
	done     chan struct{}
	stopOnce sync.Once
	workerWG sync.WaitGroup
	rng      *rand.Rand

	cfg    Config
	store  waStore
	reg    registryWriter
	hooks  eventSink
	log    *slog.Logger
	mgrCtx context.Context
}

type sendJob struct {
	ctx        context.Context
	to         string
	text       string // text body, or media caption
	typing     *typing.Override
	media      *outboundMedia // nil for text sends
	out        chan sendOutcome
	enqueuedAt time.Time
}

type sendOutcome struct {
	res *SendResult
	err error
}

// unreadKey identifies inbound messages from one sender in one chat.
type unreadKey struct{ chat, sender string }

// readTarget is the latest unread inbound message to acknowledge. phone is the
// sender's resolved phone number (when known), used to match a reply addressed
// by phone to a chat WhatsApp delivered under a LID.
type readTarget struct {
	id    string
	phone string
}

// start launches the send worker.
func (s *session) start() {
	s.workerWG.Add(1)
	go s.runWorker()
}

// stop shuts down the worker and disconnects the client.
func (s *session) stop() {
	s.stopOnce.Do(func() { close(s.done) })
	s.workerWG.Wait()
	s.mu.Lock()
	client := s.client
	pairCancel := s.pairCancel
	s.mu.Unlock()
	if pairCancel != nil {
		pairCancel()
	}
	if client != nil {
		client.Disconnect()
	}
}

func (s *session) info() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SessionInfo{
		ID:         s.id,
		Status:     s.status,
		JID:        s.jid,
		Phone:      phoneFromJID(s.jid),
		PushName:   s.pushName,
		WebhookURL: s.webhookURL,
		CreatedAt:  s.createdAt,
	}
}

func (s *session) currentJID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jid
}

func (s *session) setStatus(st Status) {
	s.mu.Lock()
	s.status = st
	s.mu.Unlock()
}

// currentQR returns the live pairing code, or ErrQRNotAvailable if none is
// available or it has expired.
func (s *session) currentQR() (*QRInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	qr := s.currentQRLocked()
	if qr == nil {
		return nil, ErrQRNotAvailable
	}
	return qr, nil
}

func (s *session) currentQRLocked() *QRInfo {
	if s.qrCode == "" {
		return nil
	}
	remaining := time.Until(s.qrExpiresAt)
	if remaining <= 0 {
		return nil
	}
	return &QRInfo{Code: s.qrCode, ExpiresAt: s.qrExpiresAt, ExpiresIn: int(remaining / time.Second)}
}

// login starts (or resumes) pairing and returns the first QR code. ctx bounds
// only the wait for that first code; pairing itself runs under the manager's
// lifetime.
func (s *session) login(ctx context.Context) (*QRInfo, error) {
	s.mu.Lock()
	switch s.status {
	case StatusConnected:
		s.mu.Unlock()
		return nil, ErrAlreadyConnected
	case StatusPairing:
		qr := s.currentQRLocked()
		s.mu.Unlock()
		if qr == nil {
			return nil, ErrQRNotAvailable
		}
		return qr, nil
	}
	s.mu.Unlock()

	client, err := s.store.NewClient()
	if err != nil {
		return nil, err
	}
	s.bindHandlers(client)

	pairCtx, cancel := context.WithTimeout(s.mgrCtx, 3*time.Minute)
	qrCh, err := client.PairQR(pairCtx)
	if err != nil {
		cancel()
		return nil, err
	}

	s.mu.Lock()
	if s.pairCancel != nil {
		s.pairCancel()
	}
	s.client = client
	s.status = StatusPairing
	s.qrCode = ""
	s.qrExpiresAt = time.Time{}
	s.pairCancel = cancel
	s.mu.Unlock()

	if err := client.Connect(); err != nil {
		cancel()
		return nil, err
	}

	first := make(chan *QRInfo, 1)
	go s.consumeQR(qrCh, cancel, first)

	select {
	case qr := <-first:
		if qr == nil {
			return nil, ErrQRNotAvailable
		}
		return qr, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(15 * time.Second):
		return nil, ErrQRNotAvailable
	}
}

// consumeQR drains the pairing channel, updating QR/connection state. After a
// terminal event it keeps reading (discarding) until the channel closes so the
// upstream forwarder never blocks.
func (s *session) consumeQR(ch <-chan qrItem, cancel context.CancelFunc, first chan<- *QRInfo) {
	defer cancel()
	sentFirst := false
	terminal := false
	signal := func(qr *QRInfo) {
		if !sentFirst {
			sentFirst = true
			first <- qr
		}
	}
	for it := range ch {
		if terminal {
			continue
		}
		switch it.Event {
		case qrEventCode:
			s.mu.Lock()
			s.qrCode = it.Code
			s.qrExpiresAt = time.Now().Add(it.Timeout)
			if s.status != StatusConnected {
				s.status = StatusPairing
			}
			qr := s.currentQRLocked()
			s.mu.Unlock()
			signal(qr)
		case qrEventSuccess:
			s.markConnected()
			signal(nil)
			terminal = true
			cancel()
		default: // timeout or error
			s.failPairing()
			signal(nil)
			terminal = true
			cancel()
		}
	}
	signal(nil)
}

func (s *session) bindHandlers(client waClient) {
	client.SetHandlers(waHandlers{
		OnMessage:      s.onMessage,
		OnConnected:    s.markConnected,
		OnDisconnected: s.onDisconnected,
		OnLoggedOut:    s.onLoggedOut,
	})
}

// markConnected records a successful connection: it captures the JID/push name,
// persists a newly-learned JID, flips status to connected, and sends an
// "available" presence so the push name registers server-side.
func (s *session) markConnected() {
	s.mu.Lock()
	client := s.client
	s.mu.Unlock()
	if client == nil {
		return
	}
	jid := client.JID()
	pushName := client.PushName()

	s.mu.Lock()
	prevJID := s.jid
	if jid != "" {
		s.jid = jid
	}
	if pushName != "" {
		s.pushName = pushName
	}
	s.status = StatusConnected
	s.qrCode = ""
	s.qrExpiresAt = time.Time{}
	s.mu.Unlock()

	if jid != "" && jid != prevJID && s.reg != nil {
		ctx, cancel := context.WithTimeout(s.mgrCtx, 5*time.Second)
		if err := s.reg.UpdateJID(ctx, s.id, jid); err != nil {
			s.log.Warn("persist jid failed", "session", s.id, "err", err)
		}
		cancel()
	}

	go func() {
		ctx, cancel := context.WithTimeout(s.mgrCtx, 10*time.Second)
		defer cancel()
		_ = client.SendAvailable(ctx)
	}()
}

func (s *session) onDisconnected() {
	s.mu.Lock()
	if s.status == StatusConnected {
		s.status = StatusDisconnected
	}
	s.mu.Unlock()
}

func (s *session) onLoggedOut() {
	s.mu.Lock()
	s.status = StatusLoggedOut
	s.qrCode = ""
	s.qrExpiresAt = time.Time{}
	s.mu.Unlock()
}

func (s *session) onMessage(eventType string, m InboundMessage) {
	if m.IsFromMe && !s.cfg.IncludeFromMe {
		return
	}
	switch eventType {
	case EventMessage:
		s.ring.Append(m)
		if !m.IsFromMe && m.MessageID != "" {
			s.trackUnread(m)
		}
	case EventEdit:
		// Reflect the new content in the buffered copy, if still present.
		s.ring.Update(m.EditedID, func(e *InboundMessage) {
			e.Text = m.Text
			e.Kind = m.Kind
			e.Media = m.Media
		})
	case EventRevoke:
		// Deleted for everyone; leave the ring as-is and just deliver the event.
	}
	s.mu.Lock()
	url := s.webhookURL
	s.mu.Unlock()
	if s.hooks != nil {
		s.hooks.Enqueue(url, Event{Event: eventType, SessionID: s.id, TS: time.Now().UTC(), Data: m})
	}
}

// trackUnread remembers the latest inbound message per chat/sender so it can be
// marked as read when we next reply to that chat.
func (s *session) trackUnread(m InboundMessage) {
	s.mu.Lock()
	if s.unread == nil {
		s.unread = make(map[unreadKey]readTarget)
	}
	s.unread[unreadKey{chat: m.Chat, sender: m.Sender}] = readTarget{id: m.MessageID, phone: m.SenderPhone}
	s.mu.Unlock()
}

// markChatRead sends read receipts (blue ticks) for pending messages in the
// chat we are about to reply to, mirroring the human "open, read, reply" flow.
// A reply is addressed by phone number, so it also matches chats WhatsApp
// delivered under a LID (matched by the sender's resolved phone). The stored
// chat/sender JIDs are used for the receipt so it lands in the right chat.
// Errors are logged, not fatal.
func (s *session) markChatRead(ctx context.Context, client waClient, to string) {
	toPhone := phoneFromJID(to)
	type target struct {
		ids          []string
		chat, sender string
	}
	var targets []target
	s.mu.Lock()
	for k, rt := range s.unread {
		if k.chat == to || (toPhone != "" && rt.phone == toPhone) {
			targets = append(targets, target{ids: []string{rt.id}, chat: k.chat, sender: k.sender})
			delete(s.unread, k)
		}
	}
	s.mu.Unlock()

	now := time.Now()
	for _, t := range targets {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		if err := client.MarkRead(rctx, t.ids, now, t.chat, t.sender); err != nil {
			s.log.Warn("mark read failed", "session", s.id, "chat", t.chat, "err", err)
		} else {
			s.log.Debug("marked read", "session", s.id, "chat", t.chat, "count", len(t.ids))
		}
		cancel()
	}
}

// failPairing aborts an in-progress pairing and resets the session to created.
func (s *session) failPairing() {
	s.mu.Lock()
	if s.status == StatusPairing {
		s.status = StatusCreated
	}
	s.qrCode = ""
	s.qrExpiresAt = time.Time{}
	client := s.client
	s.client = nil
	s.mu.Unlock()
	if client != nil {
		client.Disconnect()
	}
}

// attach loads and connects an already-paired session on boot.
func (s *session) attach() error {
	client, err := s.store.LoadClient(s.mgrCtx, s.currentJID())
	if err != nil {
		s.setStatus(StatusLoggedOut)
		return err
	}
	s.bindHandlers(client)
	s.mu.Lock()
	s.client = client
	if s.status != StatusConnected {
		s.status = StatusDisconnected
	}
	s.mu.Unlock()
	go func() {
		if err := client.Connect(); err != nil {
			s.log.Warn("connect failed", "session", s.id, "err", err)
		}
	}()
	return nil
}

// logout unlinks the device. Valid only for a paired session.
func (s *session) logout(ctx context.Context) error {
	s.mu.Lock()
	client := s.client
	status := s.status
	s.mu.Unlock()
	if client == nil || (status != StatusConnected && status != StatusDisconnected) {
		return ErrNotLoggedIn
	}
	if err := client.Logout(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.status = StatusLoggedOut
	s.qrCode = ""
	s.qrExpiresAt = time.Time{}
	s.mu.Unlock()
	return nil
}

// send validates, normalizes, and enqueues an outgoing message, then waits for
// the worker's outcome.
func (s *session) send(ctx context.Context, req SendRequest) (*SendResult, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	to, err := NormalizeRecipient(req.To)
	if err != nil {
		return nil, err
	}
	return s.enqueue(ctx, &sendJob{ctx: ctx, to: to, text: req.Text, typing: req.Typing})
}

func (s *session) sendMedia(ctx context.Context, req MediaSendRequest) (*SendResult, error) {
	if len(req.Data) == 0 {
		return nil, ErrMissingMedia
	}
	if s.cfg.MaxMediaBytes > 0 && int64(len(req.Data)) > s.cfg.MaxMediaBytes {
		return nil, ErrMediaTooLarge
	}
	if err := validateTypingOverride(req.Typing); err != nil {
		return nil, err
	}
	to, err := NormalizeRecipient(req.To)
	if err != nil {
		return nil, err
	}
	media := &outboundMedia{
		Data:     req.Data,
		Mimetype: req.Mimetype,
		Kind:     mediaKindFromMimetype(req.Mimetype),
		Caption:  req.Caption,
		Filename: req.Filename,
	}
	return s.enqueue(ctx, &sendJob{ctx: ctx, to: to, text: req.Caption, typing: req.Typing, media: media})
}

// enqueue submits a prepared job to the per-session worker and waits for its
// outcome. Sends are refused unless the session is connected, and fail fast
// with ErrQueueFull when the queue is full.
func (s *session) enqueue(ctx context.Context, job *sendJob) (*SendResult, error) {
	s.mu.Lock()
	status := s.status
	s.mu.Unlock()
	if status != StatusConnected {
		return nil, ErrNotConnected
	}
	job.out = make(chan sendOutcome, 1)
	job.enqueuedAt = time.Now()
	select {
	case s.sendCh <- job:
	default:
		return nil, ErrQueueFull
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case out := <-job.out:
		return out.res, out.err
	}
}

// downloadMedia decrypts and returns the attachment referenced by token. It
// does not go through the send queue — downloads are read-only and can run
// concurrently.
func (s *session) downloadMedia(ctx context.Context, token string) (*MediaContent, error) {
	ref, err := decodeMediaRef(token)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	client := s.client
	connected := s.status == StatusConnected
	s.mu.Unlock()
	if !connected || client == nil {
		return nil, ErrNotConnected
	}
	data, err := client.DownloadMedia(ctx, ref)
	if err != nil {
		return nil, &MediaError{Err: err}
	}
	return &MediaContent{Data: data, Mimetype: ref.Mimetype, Filename: ref.Filename}, nil
}

// editMessage replaces the text of a message we previously sent to `to`.
func (s *session) editMessage(ctx context.Context, to, messageID, newText string) (*SendResult, error) {
	if strings.TrimSpace(newText) == "" {
		return nil, ErrEmptyText
	}
	toJID, err := NormalizeRecipient(to)
	if err != nil {
		return nil, err
	}
	client, err := s.connectedClient()
	if err != nil {
		return nil, err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	res, err := client.EditText(sendCtx, toJID, messageID, newText)
	if err != nil {
		return nil, &SendError{To: toJID, Err: err}
	}
	return &SendResult{MessageID: res.ID, To: toJID, Timestamp: res.Timestamp}, nil
}

// revokeMessage deletes (for everyone) a message we previously sent to `to`.
func (s *session) revokeMessage(ctx context.Context, to, messageID string) (*SendResult, error) {
	toJID, err := NormalizeRecipient(to)
	if err != nil {
		return nil, err
	}
	client, err := s.connectedClient()
	if err != nil {
		return nil, err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	res, err := client.RevokeMessage(sendCtx, toJID, messageID)
	if err != nil {
		return nil, &SendError{To: toJID, Err: err}
	}
	return &SendResult{MessageID: res.ID, To: toJID, Timestamp: res.Timestamp}, nil
}

// connectedClient returns the session's client if it is connected.
func (s *session) connectedClient() (waClient, error) {
	s.mu.Lock()
	client := s.client
	connected := s.status == StatusConnected
	s.mu.Unlock()
	if !connected || client == nil {
		return nil, ErrNotConnected
	}
	return client, nil
}

func (s *session) runWorker() {
	defer s.workerWG.Done()
	for {
		select {
		case <-s.done:
			return
		case job := <-s.sendCh:
			s.process(job)
			if s.cfg.SendMinGap > 0 {
				select {
				case <-s.done:
					return
				case <-time.After(s.cfg.SendMinGap):
				}
			}
		}
	}
}

func (s *session) process(job *sendJob) {
	if err := job.ctx.Err(); err != nil {
		job.out <- sendOutcome{err: err}
		return
	}
	s.mu.Lock()
	client := s.client
	connected := s.status == StatusConnected
	typingCfg := s.cfg.Typing
	s.mu.Unlock()
	if !connected || client == nil {
		job.out <- sendOutcome{err: ErrNotConnected}
		return
	}

	// Human flow: open the chat (mark its messages read) before replying.
	s.markChatRead(job.ctx, client, job.to)

	queuedMS := time.Since(job.enqueuedAt).Milliseconds()

	var typingMS int64
	if dur := typing.Duration(job.text, typingCfg.Apply(job.typing), s.rng); dur > 0 {
		typingMS = dur.Milliseconds()
		if !s.simulateTyping(job.ctx, client, job.to, dur) {
			if err := job.ctx.Err(); err != nil {
				job.out <- sendOutcome{err: err}
			} else {
				job.out <- sendOutcome{err: ErrNotConnected}
			}
			return
		}
	}

	// Commit: once we send, the request must complete even if the caller goes
	// away, so we don't leave the send in an ambiguous state.
	timeout := 30 * time.Second
	if job.media != nil {
		timeout = 2 * time.Minute // uploads can be slow
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(job.ctx), timeout)
	var res waSendResult
	var err error
	if job.media != nil {
		res, err = client.SendMedia(sendCtx, job.to, *job.media)
	} else {
		res, err = client.SendText(sendCtx, job.to, job.text)
	}
	cancel()
	if err != nil {
		job.out <- sendOutcome{err: &SendError{To: job.to, Err: err}}
		return
	}
	job.out <- sendOutcome{res: &SendResult{
		MessageID: res.ID,
		To:        job.to,
		Timestamp: res.Timestamp,
		TypingMS:  typingMS,
		QueuedMS:  queuedMS,
	}}
}

// simulateTyping shows the composing indicator for dur, refreshing it every few
// seconds (the indicator decays on the recipient's phone). It returns false if
// interrupted by the caller's context or by shutdown.
func (s *session) simulateTyping(ctx context.Context, client waClient, to string, dur time.Duration) bool {
	_ = client.SetComposing(ctx, to, true)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = client.SetComposing(stopCtx, to, false)
		cancel()
	}()

	const slice = 8 * time.Second
	for remaining := dur; remaining > 0; {
		step := slice
		if remaining < step {
			step = remaining
		}
		select {
		case <-s.done:
			return false
		case <-ctx.Done():
			return false
		case <-time.After(step):
		}
		remaining -= step
		if remaining > 0 {
			_ = client.SetComposing(ctx, to, true)
		}
	}
	return true
}

// phoneFromJID extracts the digits of an individual-user JID, or "" for group
// or LID JIDs where a phone number is not directly available.
func phoneFromJID(jid string) string {
	at := strings.IndexByte(jid, '@')
	if at <= 0 || jid[at+1:] != userServer {
		return ""
	}
	user := jid[:at]
	if colon := strings.IndexByte(user, ':'); colon >= 0 {
		user = user[:colon]
	}
	return user
}
