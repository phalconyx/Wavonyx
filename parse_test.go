package wavonyx

import (
	"testing"
	"time"

	waCommon "go.mau.fi/whatsmeow/proto/waCommon"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

func user(u string) types.JID  { return types.NewJID(u, "s.whatsapp.net") }
func group(u string) types.JID { return types.NewJID(u, "g.us") }
func lid(u string) types.JID   { return types.NewJID(u, "lid") }

func TestParseConversation(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			ID:            "MSG1",
			PushName:      "Alice",
			Timestamp:     time.Unix(1_700_000_000, 0),
			MessageSource: types.MessageSource{Chat: user("6281111"), Sender: user("6282222")},
		},
		Message: &waE2E.Message{Conversation: proto.String("halo dunia")},
	}
	m, _, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.Kind != KindText || m.Text != "halo dunia" {
		t.Fatalf("kind/text: %q / %q", m.Kind, m.Text)
	}
	if m.MessageID != "MSG1" || m.PushName != "Alice" {
		t.Fatalf("meta: %+v", m)
	}
	if m.SenderPhone != "6282222" {
		t.Fatalf("sender phone: %q", m.SenderPhone)
	}
	if m.IsGroup || m.IsFromMe || m.Quoted != nil {
		t.Fatalf("flags: group=%v fromMe=%v quoted=%v", m.IsGroup, m.IsFromMe, m.Quoted)
	}
}

func TestParseExtendedTextWithQuote(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			ID:            "MSG2",
			MessageSource: types.MessageSource{Chat: user("6281111"), Sender: user("6282222")},
		},
		Message: &waE2E.Message{
			ExtendedTextMessage: &waE2E.ExtendedTextMessage{
				Text: proto.String("balasan"),
				ContextInfo: &waE2E.ContextInfo{
					StanzaID:      proto.String("QUOTED_ID"),
					Participant:   proto.String("6283333@s.whatsapp.net"),
					QuotedMessage: &waE2E.Message{Conversation: proto.String("pesan asli")},
				},
			},
		},
	}
	m, _, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.Kind != KindText || m.Text != "balasan" {
		t.Fatalf("kind/text: %q / %q", m.Kind, m.Text)
	}
	if m.Quoted == nil {
		t.Fatal("want quoted")
	}
	if m.Quoted.MessageID != "QUOTED_ID" || m.Quoted.Sender != "6283333@s.whatsapp.net" || m.Quoted.Text != "pesan asli" {
		t.Fatalf("quoted: %+v", m.Quoted)
	}
}

func TestParseImageWithMedia(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{ID: "MSG3", MessageSource: types.MessageSource{Chat: user("1"), Sender: user("2")}},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			Caption:       proto.String("look at this"),
			Mimetype:      proto.String("image/jpeg"),
			DirectPath:    proto.String("/v/img.enc"),
			MediaKey:      []byte("themediakey"),
			FileEncSHA256: []byte("encsha"),
			FileSHA256:    []byte("sha"),
			FileLength:    proto.Uint64(12345),
			Width:         proto.Uint32(640),
			Height:        proto.Uint32(480),
		}},
	}
	m, _, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.Kind != KindImage || m.Text != "look at this" {
		t.Fatalf("kind/text: %q / %q", m.Kind, m.Text)
	}
	if m.Media == nil {
		t.Fatal("want media info")
	}
	if m.Media.Mimetype != "image/jpeg" || m.Media.FileSize != 12345 || m.Media.Width != 640 || m.Media.Height != 480 {
		t.Fatalf("media meta: %+v", m.Media)
	}
	// The token must round-trip to a downloadable ref.
	ref, err := decodeMediaRef(m.Media.Token)
	if err != nil {
		t.Fatalf("token decode: %v", err)
	}
	if ref.Kind != KindImage || ref.DirectPath != "/v/img.enc" || string(ref.MediaKey) != "themediakey" || ref.Mimetype != "image/jpeg" {
		t.Fatalf("decoded ref: %+v", ref)
	}
}

func TestParseVoiceAndDocument(t *testing.T) {
	// PTT audio -> voice kind, no text.
	audio := &events.Message{
		Info: types.MessageInfo{ID: "A", MessageSource: types.MessageSource{Chat: user("1"), Sender: user("2")}},
		Message: &waE2E.Message{AudioMessage: &waE2E.AudioMessage{
			PTT: proto.Bool(true), Mimetype: proto.String("audio/ogg"),
			DirectPath: proto.String("/v/a"), MediaKey: []byte("k"), Seconds: proto.Uint32(7),
		}},
	}
	if m, _, ok := parseMessage(audio); !ok || m.Kind != KindVoice || m.Text != "" || m.Media == nil || m.Media.Duration != 7 {
		t.Fatalf("voice: ok=%v kind=%q media=%+v", ok, m.Kind, m.Media)
	}

	// Document -> document kind with filename.
	doc := &events.Message{
		Info: types.MessageInfo{ID: "D", MessageSource: types.MessageSource{Chat: user("1"), Sender: user("2")}},
		Message: &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
			Mimetype: proto.String("application/pdf"), FileName: proto.String("invoice.pdf"),
			DirectPath: proto.String("/v/d"), MediaKey: []byte("k"), Caption: proto.String("here"),
		}},
	}
	m, _, ok := parseMessage(doc)
	if !ok || m.Kind != KindDocument || m.Text != "here" || m.Media == nil || m.Media.Filename != "invoice.pdf" {
		t.Fatalf("document: ok=%v kind=%q media=%+v", ok, m.Kind, m.Media)
	}
}

func TestParseGroupAndFromMe(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			ID:            "MSG4",
			MessageSource: types.MessageSource{Chat: group("120363111"), Sender: user("6282222"), IsGroup: true, IsFromMe: true},
		},
		Message: &waE2E.Message{Conversation: proto.String("hai grup")},
	}
	m, _, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if !m.IsGroup || !m.IsFromMe {
		t.Fatalf("flags: group=%v fromMe=%v", m.IsGroup, m.IsFromMe)
	}
	if m.Chat != "120363111@g.us" {
		t.Fatalf("chat: %q", m.Chat)
	}
}

func TestParseLIDSenderResolvesPhoneFromAlt(t *testing.T) {
	evt := &events.Message{
		Info: types.MessageInfo{
			ID: "MSG5",
			MessageSource: types.MessageSource{
				Chat:      group("120363111"),
				Sender:    lid("111222333"),
				SenderAlt: user("6284444"),
				IsGroup:   true,
			},
		},
		Message: &waE2E.Message{Conversation: proto.String("dari lid")},
	}
	m, _, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.SenderPhone != "6284444" {
		t.Fatalf("sender phone from alt: %q", m.SenderPhone)
	}
}

func TestParseNoTextSkipped(t *testing.T) {
	// Media without a caption is still emitted (it has content); only messages
	// with no user-facing content at all are skipped.
	cases := map[string]*events.Message{
		"nil message":   {Info: types.MessageInfo{ID: "x"}, Message: nil},
		"empty message": {Info: types.MessageInfo{ID: "x"}, Message: &waE2E.Message{}},
		"ephemeral protocol": {Info: types.MessageInfo{ID: "x"}, Message: &waE2E.Message{
			ProtocolMessage: &waE2E.ProtocolMessage{Type: waE2E.ProtocolMessage_EPHEMERAL_SETTING.Enum()},
		}},
	}
	for name, evt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, ok := parseMessage(evt); ok {
				t.Fatal("want skipped (ok=false)")
			}
		})
	}
}

func TestParseEventTypes(t *testing.T) {
	src := types.MessageSource{Chat: user("1"), Sender: user("2")}

	if _, evType, ok := parseMessage(&events.Message{
		Info: types.MessageInfo{ID: "N", MessageSource: src}, Message: &waE2E.Message{Conversation: proto.String("hi")},
	}); !ok || evType != EventMessage {
		t.Fatalf("normal: ok=%v evType=%q", ok, evType)
	}

	// Unwrapped edit: IsEdit set, content in Message, target from Info.ID.
	m, evType, ok := parseMessage(&events.Message{
		Info: types.MessageInfo{ID: "ORIG1", MessageSource: src}, IsEdit: true,
		Message: &waE2E.Message{Conversation: proto.String("edited text")},
	})
	if !ok || evType != EventEdit || m.EditedID != "ORIG1" || m.Text != "edited text" {
		t.Fatalf("unwrapped edit: ok=%v evType=%q editedID=%q text=%q", ok, evType, m.EditedID, m.Text)
	}

	// Raw edit via ProtocolMessage: target from Key.ID, content from EditedMessage.
	m, evType, ok = parseMessage(&events.Message{
		Info: types.MessageInfo{ID: "EDITMSG", MessageSource: src},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type:          waE2E.ProtocolMessage_MESSAGE_EDIT.Enum(),
			Key:           &waCommon.MessageKey{ID: proto.String("ORIG2")},
			EditedMessage: &waE2E.Message{Conversation: proto.String("new content")},
		}},
	})
	if !ok || evType != EventEdit || m.EditedID != "ORIG2" || m.Text != "new content" {
		t.Fatalf("raw edit: ok=%v evType=%q editedID=%q text=%q", ok, evType, m.EditedID, m.Text)
	}

	// Revoke: MessageID is the deleted message's id.
	m, evType, ok = parseMessage(&events.Message{
		Info: types.MessageInfo{ID: "REVMSG", MessageSource: src},
		Message: &waE2E.Message{ProtocolMessage: &waE2E.ProtocolMessage{
			Type: waE2E.ProtocolMessage_REVOKE.Enum(),
			Key:  &waCommon.MessageKey{ID: proto.String("DELETED")},
		}},
	})
	if !ok || evType != EventRevoke || m.MessageID != "DELETED" {
		t.Fatalf("revoke: ok=%v evType=%q id=%q", ok, evType, m.MessageID)
	}
}
