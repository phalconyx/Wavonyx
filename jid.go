package wavonyx

import "strings"

// userServer is the JID server for individual WhatsApp users.
const userServer = "s.whatsapp.net"

// NormalizeRecipient turns a caller-supplied recipient into a WhatsApp JID
// string. It is pure string handling; the whatsmeow adapter performs the final
// types.ParseJID. Two input forms are accepted:
//
//   - An explicit JID containing "@" (e.g. "628...@s.whatsapp.net" or a group
//     "...@g.us"): checked for a non-empty user part, no embedded whitespace,
//     and a known server, then returned unchanged.
//   - A phone number in international form, with or without a leading "+" and
//     common separators (spaces, "-", ".", "(", ")"): must be 7–15 digits
//     including the country code. Local formats with a leading "0" are rejected
//     because the country code cannot be inferred.
//
// Anything else yields ErrInvalidRecipient.
func NormalizeRecipient(input string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", ErrInvalidRecipient
	}
	if strings.Contains(s, "@") {
		return normalizeJID(s)
	}
	return normalizePhone(s)
}

func normalizePhone(s string) (string, error) {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' || r == ' ' || r == '-' || r == '.' || r == '(' || r == ')':
			// separators are ignored
		default:
			return "", ErrInvalidRecipient
		}
	}
	digits := b.String()
	if len(digits) < 7 || len(digits) > 15 {
		return "", ErrInvalidRecipient
	}
	if digits[0] == '0' {
		return "", ErrInvalidRecipient // local format, no country code
	}
	return digits + "@" + userServer, nil
}

func normalizeJID(s string) (string, error) {
	if strings.ContainsAny(s, " \t\r\n") {
		return "", ErrInvalidRecipient
	}
	at := strings.IndexByte(s, '@')
	user, server := s[:at], s[at+1:]
	if user == "" || server == "" || strings.ContainsRune(server, '@') {
		return "", ErrInvalidRecipient
	}
	switch server {
	case userServer, "g.us", "lid":
		return s, nil
	default:
		return "", ErrInvalidRecipient
	}
}
