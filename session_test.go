package wavonyx

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/phalconyx/wavonyx/internal/registry"
	"github.com/phalconyx/wavonyx/typing"
)

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// pairScript is the common "one code then success" QR script.
func pairScript(code string) []qrItem {
	return []qrItem{
		{Event: qrEventCode, Code: code, Timeout: time.Second},
		{Event: qrEventSuccess},
	}
}

func TestPairingSuccessPersistsJID(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("CODE1")
	store.pairJID = "628123@s.whatsapp.net"
	store.pairPush = "Fajar"
	m, reg := newTestManager(t, store, Config{})

	create(t, m, "personal")
	qr, err := m.Login(ctx, "personal")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if qr.Code != "CODE1" {
		t.Fatalf("first qr code: %q", qr.Code)
	}

	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)
	info, _ := m.Get(ctx, "personal")
	if info.JID != "628123@s.whatsapp.net" || info.Phone != "628123" || info.PushName != "Fajar" {
		t.Fatalf("connected info: %+v", info)
	}
	if row, _ := reg.Get(ctx, "personal"); row.JID != "628123@s.whatsapp.net" {
		t.Fatalf("registry jid: %q", row.JID)
	}
}

func TestPairingQRRotation(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = []qrItem{
		{Event: qrEventCode, Code: "CODE1", Timeout: time.Second},
		{Event: qrEventCode, Code: "CODE2", Timeout: time.Second},
	}
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	qr, err := m.Login(ctx, "personal")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if qr.Code != "CODE1" {
		t.Fatalf("first code: %q", qr.Code)
	}

	waitUntil(t, func() bool {
		q, err := m.QR(ctx, "personal")
		return err == nil && q.Code == "CODE2"
	}, 2*time.Second)

	if info, _ := m.Get(ctx, "personal"); info.Status != StatusPairing {
		t.Fatalf("status after rotation: %v", info.Status)
	}
}

func TestLoginAlreadyConnected(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)
	if _, err := m.Login(ctx, "personal"); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("second login: %v", err)
	}
}

func TestQRNotAvailableBeforeLogin(t *testing.T) {
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")
	if _, err := m.QR(context.Background(), "personal"); !errors.Is(err, ErrQRNotAvailable) {
		t.Fatalf("want ErrQRNotAvailable, got %v", err)
	}
}

func TestSendWithConstantTyping(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	cfg := Config{
		Typing:     typing.Config{Mode: typing.ModeConstant, PerChar: 10 * time.Millisecond, MinTotal: time.Millisecond, MaxTotal: time.Minute},
		SendMinGap: 0,
	}
	m, _ := newTestManager(t, store, cfg)

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.resetCalls()
	res, err := m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "hello"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if res.MessageID != "FAKEID" || res.To != "628999888777@s.whatsapp.net" {
		t.Fatalf("send result: %+v", res)
	}
	if res.TypingMS != 50 { // 5 runes * 10ms
		t.Fatalf("typing ms=%d want 50", res.TypingMS)
	}
	if got := fc.actions(); !equalStr(got, []string{"composing", "paused", "send:hello"}) {
		t.Fatalf("call order: %v", got)
	}
}

func TestSendValidationErrors(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")

	if _, err := m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "   "}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("empty text: %v", err)
	}
	if _, err := m.Send(ctx, "personal", SendRequest{To: "08123456789", Text: "hi"}); !errors.Is(err, ErrInvalidRecipient) {
		t.Fatalf("bad recipient: %v", err)
	}
}

func TestSendNotConnected(t *testing.T) {
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")
	if _, err := m.Send(context.Background(), "personal", SendRequest{To: "628999888777", Text: "hi"}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestSendQueueFull(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	cfg := Config{Typing: typing.Config{Mode: typing.ModeOff}, SendMinGap: 0, SendQueue: 1}
	m, _ := newTestManager(t, store, cfg)

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	entered := make(chan string, 10)
	gate := make(chan struct{})
	fc.setGate(entered, gate)

	s, _ := m.session("personal")
	go func() { _, _ = m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "one"}) }()
	<-entered // worker is now blocked delivering job 1
	go func() { _, _ = m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "two"}) }()
	waitUntil(t, func() bool { return len(s.sendCh) == 1 }, time.Second)

	if _, err := m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "three"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
	close(gate)
}

func TestInboundMessageRingBuffer(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{IncludeFromMe: false})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.fireMessage(InboundMessage{MessageID: "A", Text: "hai"})
	fc.fireMessage(InboundMessage{MessageID: "B", Text: "own", IsFromMe: true}) // dropped
	fc.fireMessage(InboundMessage{MessageID: "C", Text: "lagi"})

	recent, err := m.Recent(ctx, "personal", 10)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(recent) != 2 || recent[0].MessageID != "C" || recent[1].MessageID != "A" {
		t.Fatalf("ring contents: %v", ids(recent))
	}
}

func TestMarkReadOnReply(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	cfg := Config{Typing: typing.Config{Mode: typing.ModeOff}, SendMinGap: 0}
	m, _ := newTestManager(t, store, cfg)

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.fireMessage(InboundMessage{MessageID: "IN1", Chat: "628999888777@s.whatsapp.net", Sender: "628999888777@s.whatsapp.net", Text: "hi", Timestamp: time.Now()})
	fc.resetCalls()

	if _, err := m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "halo"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	// The chat is marked read just before replying.
	if got := fc.actions(); !equalStr(got, []string{"read:628999888777@s.whatsapp.net", "send:halo"}) {
		t.Fatalf("call order: %v", got)
	}
}

func TestMarkReadMatchesLIDByPhone(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	cfg := Config{Typing: typing.Config{Mode: typing.ModeOff}, SendMinGap: 0}
	m, _ := newTestManager(t, store, cfg)

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	// Delivered under a LID; the phone is resolved via SenderAlt (parse.go).
	fc.fireMessage(InboundMessage{MessageID: "IN1", Chat: "111222@lid", Sender: "111222@lid", SenderPhone: "628999888777", Text: "hi"})
	fc.resetCalls()

	// Replying by phone must still mark the LID chat read (matched by phone),
	// using the original LID JID for the receipt.
	if _, err := m.Send(ctx, "personal", SendRequest{To: "628999888777", Text: "halo"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := fc.actions(); !equalStr(got, []string{"read:111222@lid", "send:halo"}) {
		t.Fatalf("call order: %v", got)
	}
}

func TestDownloadMedia(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	token := encodeMediaRef(mediaRef{Kind: KindImage, DirectPath: "/v/x", MediaKey: []byte("k"), Mimetype: "image/png"})
	content, err := m.DownloadMedia(ctx, "personal", token)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if string(content.Data) != "FAKEMEDIA:image" || content.Mimetype != "image/png" {
		t.Fatalf("content: data=%q mimetype=%q", content.Data, content.Mimetype)
	}

	if _, err := m.DownloadMedia(ctx, "personal", "!!!not-a-token"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token: got %v", err)
	}
}

func TestDownloadMediaNotConnected(t *testing.T) {
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")
	token := encodeMediaRef(mediaRef{Kind: KindImage, DirectPath: "/v/x", MediaKey: []byte("k")})
	if _, err := m.DownloadMedia(context.Background(), "personal", token); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("want ErrNotConnected, got %v", err)
	}
}

func TestSendMedia(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{Typing: typing.Config{Mode: typing.ModeOff}, SendMinGap: 0})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.resetCalls()
	res, err := m.SendMedia(ctx, "personal", MediaSendRequest{
		To: "628999888777", Caption: "check", Data: []byte("JPEGDATA"), Mimetype: "image/jpeg", Filename: "a.jpg",
	})
	if err != nil {
		t.Fatalf("send media: %v", err)
	}
	if res.MessageID != "FAKEID" || res.To != "628999888777@s.whatsapp.net" {
		t.Fatalf("result: %+v", res)
	}
	if got := fc.actions(); !equalStr(got, []string{"sendmedia:image"}) {
		t.Fatalf("calls: %v", got)
	}
}

func TestSendMediaTooLarge(t *testing.T) {
	m, _ := newTestManager(t, newFakeStore(), Config{MaxMediaBytes: 4})
	create(t, m, "personal")
	if _, err := m.SendMedia(context.Background(), "personal", MediaSendRequest{To: "628999888777", Data: []byte("toolong"), Mimetype: "image/jpeg"}); !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("want ErrMediaTooLarge, got %v", err)
	}
}

func TestSendMediaMissing(t *testing.T) {
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")
	if _, err := m.SendMedia(context.Background(), "personal", MediaSendRequest{To: "628999888777", Mimetype: "image/jpeg"}); !errors.Is(err, ErrMissingMedia) {
		t.Fatalf("want ErrMissingMedia, got %v", err)
	}
}

func TestEditUpdatesRing(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, sink := newTestManagerSink(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.fireMessage(InboundMessage{MessageID: "M1", Chat: "628999888777@s.whatsapp.net", Sender: "628999888777@s.whatsapp.net", Kind: KindText, Text: "original"})
	fc.fireEvent(EventEdit, InboundMessage{MessageID: "EDIT1", EditedID: "M1", Kind: KindText, Text: "corrected"})

	recent, _ := m.Recent(ctx, "personal", 10)
	if len(recent) != 1 || recent[0].MessageID != "M1" || recent[0].Text != "corrected" {
		t.Fatalf("ring after edit: %+v", recent)
	}
	evs := sink.all()
	if len(evs) != 2 || evs[0].Event != EventMessage || evs[1].Event != EventEdit || evs[1].Data.EditedID != "M1" {
		t.Fatalf("events: %+v", evs)
	}
}

func TestRevokeEvent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, sink := newTestManagerSink(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	store.client().fireEvent(EventRevoke, InboundMessage{MessageID: "DELETED", Chat: "628999888777@s.whatsapp.net", Sender: "628999888777@s.whatsapp.net"})

	if evs := sink.all(); len(evs) != 1 || evs[0].Event != EventRevoke || evs[0].Data.MessageID != "DELETED" {
		t.Fatalf("revoke events: %+v", evs)
	}
	if recent, _ := m.Recent(ctx, "personal", 10); len(recent) != 0 {
		t.Fatalf("ring should stay empty after revoke: %+v", recent)
	}
}

func TestUpdateWebhookPerSession(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, sink := newTestManagerSink(t, store, Config{})

	// Two sessions, each pointing somewhere different.
	if _, err := m.Create(ctx, "toko", "https://app.example/wh/toko"); err != nil {
		t.Fatalf("create toko: %v", err)
	}
	if _, err := m.Create(ctx, "cs", ""); err != nil {
		t.Fatalf("create cs: %v", err)
	}

	info, err := m.UpdateWebhook(ctx, "cs", "https://app.example/wh/cs")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if info.WebhookURL != "https://app.example/wh/cs" {
		t.Fatalf("info: %+v", info)
	}
	// The change must survive a manager restart, i.e. be persisted.
	if got, _ := m.Get(ctx, "cs"); got.WebhookURL != "https://app.example/wh/cs" {
		t.Fatalf("not applied to the live session: %+v", got)
	}

	// An inbound message on that session must go to its own URL.
	if _, err := m.Login(ctx, "cs"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "cs", StatusConnected, 2*time.Second)
	store.client().fireMessage(InboundMessage{MessageID: "M1", Chat: "628999888777@s.whatsapp.net", Text: "hi"})

	waitUntil(t, func() bool { return len(sink.all()) > 0 }, 2*time.Second)
	if got := sink.urls(); len(got) != 1 || got[0] != "https://app.example/wh/cs" {
		t.Fatalf("delivered to %v", got)
	}

	// Clearing falls back to the global webhook (empty URL at the dispatcher).
	if info, err = m.UpdateWebhook(ctx, "cs", ""); err != nil || info.WebhookURL != "" {
		t.Fatalf("clear: info=%+v err=%v", info, err)
	}
}

func TestUpdateWebhookValidation(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m, "personal")

	for _, bad := range []string{"not-a-url", "ftp://x/y", "https://"} {
		if _, err := m.UpdateWebhook(ctx, "personal", bad); !errors.Is(err, ErrInvalidWebhook) {
			t.Fatalf("%q: want ErrInvalidWebhook, got %v", bad, err)
		}
	}
	if _, err := m.UpdateWebhook(ctx, "missing", "https://x/y"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	// Create validates too.
	if _, err := m.Create(ctx, "bad", "not-a-url"); !errors.Is(err, ErrInvalidWebhook) {
		t.Fatalf("create with bad url: %v", err)
	}
}

func TestPushNameRefreshesAfterConnect(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	store.pairPush = "" // WhatsApp has not told us the name yet at connect time
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	if info, _ := m.Get(ctx, "personal"); info.PushName != "" {
		t.Fatalf("push name should still be unknown: %q", info.PushName)
	}

	// It arrives later, as it does in practice via app-state sync.
	store.client().setPushName("Phalconyx")
	if info, _ := m.Get(ctx, "personal"); info.PushName != "Phalconyx" {
		t.Fatalf("push name not picked up: %q", info.PushName)
	}

	// A rename on the phone is reflected too.
	store.client().setPushName("Phalconyx Store")
	if info, _ := m.Get(ctx, "personal"); info.PushName != "Phalconyx Store" {
		t.Fatalf("rename not picked up: %q", info.PushName)
	}
}

func TestEditMessageSend(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})
	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.resetCalls()
	res, err := m.EditMessage(ctx, "personal", "628999888777", "MID1", "corrected")
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if res.To != "628999888777@s.whatsapp.net" || res.MessageID != "FAKEID" {
		t.Fatalf("result: %+v", res)
	}
	if got := fc.actions(); !equalStr(got, []string{"edit:MID1"}) {
		t.Fatalf("calls: %v", got)
	}
	if _, err := m.EditMessage(ctx, "personal", "628999888777", "MID1", "   "); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("empty edit: %v", err)
	}
}

func TestRevokeMessageSend(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})
	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	fc := store.client()
	fc.resetCalls()
	if _, err := m.RevokeMessage(ctx, "personal", "628999888777", "MID2"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if got := fc.actions(); !equalStr(got, []string{"revoke:MID2"}) {
		t.Fatalf("calls: %v", got)
	}

	m2, _ := newTestManager(t, newFakeStore(), Config{})
	create(t, m2, "x")
	if _, err := m2.RevokeMessage(ctx, "x", "628999888777", "MID"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("revoke not connected: %v", err)
	}
}

func TestLoggedOutEvent(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	store.client().fireLoggedOut()
	waitStatus(t, m, "personal", StatusLoggedOut, 2*time.Second)
}

func TestLogout(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, _ := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if err := m.Logout(ctx, "personal"); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("logout before pairing: %v", err)
	}

	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	if err := m.Logout(ctx, "personal"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	waitStatus(t, m, "personal", StatusLoggedOut, 2*time.Second)
	if jids, _ := store.LoggedInJIDs(ctx); len(jids) != 0 {
		t.Fatalf("device still linked: %v", jids)
	}
}

func TestDeleteTearsDown(t *testing.T) {
	ctx := context.Background()
	store := newFakeStore()
	store.script = pairScript("C")
	store.pairJID = "628000@s.whatsapp.net"
	m, reg := newTestManager(t, store, Config{})

	create(t, m, "personal")
	if _, err := m.Login(ctx, "personal"); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitStatus(t, m, "personal", StatusConnected, 2*time.Second)

	if err := m.Delete(ctx, "personal"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.Get(ctx, "personal"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if _, err := reg.Get(ctx, "personal"); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("registry after delete: %v", err)
	}
	if jids, _ := store.LoggedInJIDs(ctx); len(jids) != 0 {
		t.Fatalf("device not removed: %v", jids)
	}
}
