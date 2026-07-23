package wavonyx

import (
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// parseMessage classifies a whatsmeow message event and converts it into an
// InboundMessage plus the webhook event type (EventMessage, EventEdit, or
// EventRevoke). It returns ok=false for events with no user-facing content
// (reactions, sync/protocol messages, etc.), which are skipped.
//
// It is pure over the event struct: parse_test.go constructs events.Message
// values directly, so no network or whatsmeow client is involved.
func parseMessage(evt *events.Message) (InboundMessage, string, bool) {
	if evt == nil || evt.Message == nil {
		return InboundMessage{}, "", false
	}

	// A ProtocolMessage carries edits and revokes (and various sync messages we
	// ignore). Guard on nil first: ProtocolMessage_REVOKE is the enum zero
	// value, so GetType() on a plain message would otherwise look like REVOKE.
	if pm := evt.Message.GetProtocolMessage(); pm != nil {
		switch pm.GetType() {
		case waE2E.ProtocolMessage_MESSAGE_EDIT:
			content := pm.GetEditedMessage()
			if content == nil {
				return InboundMessage{}, "", false
			}
			return parseEdit(evt, content, pm.GetKey().GetID())
		case waE2E.ProtocolMessage_REVOKE:
			if id := pm.GetKey().GetID(); id != "" {
				m := buildInbound(evt.Info, "", "", nil, nil)
				m.MessageID = id // the message that was deleted
				return m, EventRevoke, true
			}
		}
		return InboundMessage{}, "", false // other protocol messages: not user content
	}

	// whatsmeow may instead unwrap an edit, flagging it and putting the new
	// content directly in evt.Message. The edited message's id is best-effort
	// from Info.ID here (no ProtocolMessage to read Key.ID from).
	if evt.IsEdit {
		return parseEdit(evt, evt.Message, evt.Info.ID)
	}

	kind, text, media, ok := extractContent(evt.Message)
	if !ok {
		return InboundMessage{}, "", false
	}
	return buildInbound(evt.Info, kind, text, media, evt.Message), EventMessage, true
}

// parseEdit builds an EventEdit from the edit's new content and the id of the
// message being changed.
func parseEdit(evt *events.Message, content *waE2E.Message, editedID string) (InboundMessage, string, bool) {
	kind, text, media, ok := extractContent(content)
	if !ok {
		return InboundMessage{}, "", false
	}
	m := buildInbound(evt.Info, kind, text, media, content)
	m.EditedID = editedID
	return m, EventEdit, true
}

// buildInbound assembles the common InboundMessage fields. msg (may be nil) is
// used only to extract any quoted message.
func buildInbound(info types.MessageInfo, kind, text string, media *MediaInfo, msg *waE2E.Message) InboundMessage {
	m := InboundMessage{
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
		Media:       media,
	}
	if msg != nil {
		m.Quoted = extractQuoted(msg)
	}
	return m
}

// extractContent classifies a message and pulls out its text and/or media.
// Protobuf getters are nil-safe, so the chain works regardless of which field
// is set. Media captions are returned as text.
func extractContent(msg *waE2E.Message) (kind, text string, media *MediaInfo, ok bool) {
	switch {
	case msg.GetConversation() != "":
		return KindText, msg.GetConversation(), nil, true

	case msg.GetExtendedTextMessage().GetText() != "":
		return KindText, msg.GetExtendedTextMessage().GetText(), nil, true

	case msg.GetImageMessage() != nil:
		im := msg.GetImageMessage()
		ref := mediaRef{Kind: KindImage, URL: im.GetURL(), DirectPath: im.GetDirectPath(), MediaKey: im.GetMediaKey(), EncSHA256: im.GetFileEncSHA256(), SHA256: im.GetFileSHA256(), FileLength: im.GetFileLength(), Mimetype: im.GetMimetype()}
		info := &MediaInfo{Mimetype: im.GetMimetype(), FileSize: im.GetFileLength(), Width: int(im.GetWidth()), Height: int(im.GetHeight()), Token: encodeMediaRef(ref)}
		return KindImage, im.GetCaption(), info, true

	case msg.GetVideoMessage() != nil:
		vm := msg.GetVideoMessage()
		ref := mediaRef{Kind: KindVideo, URL: vm.GetURL(), DirectPath: vm.GetDirectPath(), MediaKey: vm.GetMediaKey(), EncSHA256: vm.GetFileEncSHA256(), SHA256: vm.GetFileSHA256(), FileLength: vm.GetFileLength(), Mimetype: vm.GetMimetype()}
		info := &MediaInfo{Mimetype: vm.GetMimetype(), FileSize: vm.GetFileLength(), Width: int(vm.GetWidth()), Height: int(vm.GetHeight()), Duration: int(vm.GetSeconds()), Token: encodeMediaRef(ref)}
		return KindVideo, vm.GetCaption(), info, true

	case msg.GetAudioMessage() != nil:
		am := msg.GetAudioMessage()
		kind := KindAudio
		if am.GetPTT() {
			kind = KindVoice
		}
		ref := mediaRef{Kind: kind, URL: am.GetURL(), DirectPath: am.GetDirectPath(), MediaKey: am.GetMediaKey(), EncSHA256: am.GetFileEncSHA256(), SHA256: am.GetFileSHA256(), FileLength: am.GetFileLength(), Mimetype: am.GetMimetype()}
		info := &MediaInfo{Mimetype: am.GetMimetype(), FileSize: am.GetFileLength(), Duration: int(am.GetSeconds()), Token: encodeMediaRef(ref)}
		return kind, "", info, true

	case msg.GetDocumentMessage() != nil:
		dm := msg.GetDocumentMessage()
		ref := mediaRef{Kind: KindDocument, URL: dm.GetURL(), DirectPath: dm.GetDirectPath(), MediaKey: dm.GetMediaKey(), EncSHA256: dm.GetFileEncSHA256(), SHA256: dm.GetFileSHA256(), FileLength: dm.GetFileLength(), Mimetype: dm.GetMimetype(), Filename: dm.GetFileName()}
		info := &MediaInfo{Mimetype: dm.GetMimetype(), FileSize: dm.GetFileLength(), Filename: dm.GetFileName(), Token: encodeMediaRef(ref)}
		return KindDocument, dm.GetCaption(), info, true

	case msg.GetStickerMessage() != nil:
		sm := msg.GetStickerMessage()
		ref := mediaRef{Kind: KindSticker, URL: sm.GetURL(), DirectPath: sm.GetDirectPath(), MediaKey: sm.GetMediaKey(), EncSHA256: sm.GetFileEncSHA256(), SHA256: sm.GetFileSHA256(), FileLength: sm.GetFileLength(), Mimetype: sm.GetMimetype()}
		info := &MediaInfo{Mimetype: sm.GetMimetype(), FileSize: sm.GetFileLength(), Width: int(sm.GetWidth()), Height: int(sm.GetHeight()), Token: encodeMediaRef(ref)}
		return KindSticker, "", info, true
	}

	return "", "", nil, false
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
		if _, t, _, ok := extractContent(quoted); ok {
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
