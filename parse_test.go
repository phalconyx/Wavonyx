package wavonyx

import (
	"testing"
	"time"

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
	m, ok := parseMessage(evt)
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
	m, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.Kind != KindExtendedText || m.Text != "balasan" {
		t.Fatalf("kind/text: %q / %q", m.Kind, m.Text)
	}
	if m.Quoted == nil {
		t.Fatal("want quoted")
	}
	if m.Quoted.MessageID != "QUOTED_ID" || m.Quoted.Sender != "6283333@s.whatsapp.net" || m.Quoted.Text != "pesan asli" {
		t.Fatalf("quoted: %+v", m.Quoted)
	}
}

func TestParseImageCaption(t *testing.T) {
	evt := &events.Message{
		Info:    types.MessageInfo{ID: "MSG3", MessageSource: types.MessageSource{Chat: user("1"), Sender: user("2")}},
		Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{Caption: proto.String("lihat ini")}},
	}
	m, ok := parseMessage(evt)
	if !ok || m.Kind != KindImageCaption || m.Text != "lihat ini" {
		t.Fatalf("image caption: ok=%v kind=%q text=%q", ok, m.Kind, m.Text)
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
	m, ok := parseMessage(evt)
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
	m, ok := parseMessage(evt)
	if !ok {
		t.Fatal("want ok")
	}
	if m.SenderPhone != "6284444" {
		t.Fatalf("sender phone from alt: %q", m.SenderPhone)
	}
}

func TestParseNoTextSkipped(t *testing.T) {
	cases := map[string]*events.Message{
		"nil message":   {Info: types.MessageInfo{ID: "x"}, Message: nil},
		"empty message": {Info: types.MessageInfo{ID: "x"}, Message: &waE2E.Message{}},
		"empty caption": {Info: types.MessageInfo{ID: "x"}, Message: &waE2E.Message{ImageMessage: &waE2E.ImageMessage{}}},
	}
	for name, evt := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseMessage(evt); ok {
				t.Fatal("want skipped (ok=false)")
			}
		})
	}
}
