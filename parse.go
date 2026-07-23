package wavonyx

import (
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// parseMessage converts a whatsmeow message event into an InboundMessage. It
// returns ok=false for messages with no extractable text (stickers, audio,
// reactions, protocol messages, etc.), which v1 skips entirely.
//
// It is pure over the event struct: parse_test.go constructs events.Message
// values directly, so no network or whatsmeow client is involved.
func parseMessage(evt *events.Message) (InboundMessage, bool) {
	if evt == nil || evt.Message == nil {
		return InboundMessage{}, false
	}
	kind, text, ok := extractText(evt.Message)
	if !ok {
		return InboundMessage{}, false
	}

	info := evt.Info
	return InboundMessage{
		MessageID:   info.ID,
		Chat:        info.Chat.ToNonAD().String(),
		Sender:      info.Sender.ToNonAD().String(),
		SenderPhone: resolvePhone(info.Sender, info.SenderAlt),
		PushName:    info.PushName,
		IsGroup:     info.IsGroup,
		IsFromMe:    info.IsFromMe,
		Timestamp:   info.Timestamp,
		Kind:        kind,
		Text:        text,
		Quoted:      extractQuoted(evt.Message),
	}, true
}

// extractText returns the message's text content and a kind tag. Protobuf
// getters are nil-safe, so the chain works regardless of which field is set.
func extractText(msg *waE2E.Message) (kind, text string, ok bool) {
	if c := msg.GetConversation(); c != "" {
		return KindText, c, true
	}
	if t := msg.GetExtendedTextMessage().GetText(); t != "" {
		return KindExtendedText, t, true
	}
	if c := msg.GetImageMessage().GetCaption(); c != "" {
		return KindImageCaption, c, true
	}
	if c := msg.GetVideoMessage().GetCaption(); c != "" {
		return KindVideoCaption, c, true
	}
	if c := msg.GetDocumentMessage().GetCaption(); c != "" {
		return KindDocumentCaption, c, true
	}
	return "", "", false
}

// extractQuoted returns the quoted (replied-to) message, if any.
func extractQuoted(msg *waE2E.Message) *Quoted {
	ci := contextInfoOf(msg)
	if ci == nil {
		return nil
	}
	stanza := ci.GetStanzaID()
	participant := ci.GetParticipant()
	quoted := ci.GetQuotedMessage()
	if stanza == "" && participant == "" && quoted == nil {
		return nil
	}
	q := &Quoted{MessageID: stanza, Sender: participant}
	if quoted != nil {
		if _, t, ok := extractText(quoted); ok {
			q.Text = t
		}
	}
	return q
}

// contextInfoOf returns the ContextInfo of whichever sub-message carries one.
func contextInfoOf(msg *waE2E.Message) *waE2E.ContextInfo {
	if ci := msg.GetExtendedTextMessage().GetContextInfo(); ci != nil {
		return ci
	}
	if ci := msg.GetImageMessage().GetContextInfo(); ci != nil {
		return ci
	}
	if ci := msg.GetVideoMessage().GetContextInfo(); ci != nil {
		return ci
	}
	if ci := msg.GetDocumentMessage().GetContextInfo(); ci != nil {
		return ci
	}
	return nil
}

// resolvePhone returns the sender's phone number (digits only) when it can be
// determined. WhatsApp increasingly addresses senders by LID rather than phone
// number; in that case the phone form, if known, is carried in SenderAlt.
func resolvePhone(sender, senderAlt types.JID) string {
	if sender.Server == userServer {
		return sender.User
	}
	if senderAlt.Server == userServer {
		return senderAlt.User
	}
	return ""
}
