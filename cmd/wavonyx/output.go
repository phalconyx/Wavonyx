package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/phalconyx/wavonyx"
)

// commonOpts are the flags every server-facing subcommand accepts.
type commonOpts struct {
	url     *string
	key     *string
	json    *bool
	timeout *time.Duration
}

// registerCommon adds the shared flags to fs. Note that Go's flag package stops
// parsing at the first positional argument, so flags go before them.
func registerCommon(fs *flag.FlagSet) *commonOpts {
	return &commonOpts{
		url:     fs.String("url", "", "Wavonyx server URL (default $WAVONYX_URL, else derived from $WAVONYX_ADDR)"),
		key:     fs.String("key", "", "API key sent as X-API-Key (default $WAVONYX_API_KEY)"),
		json:    fs.Bool("json", false, "print raw JSON instead of a table"),
		timeout: fs.Duration("timeout", 60*time.Second, "HTTP request timeout"),
	}
}

// client builds a client from the parsed flags and the environment.
func (o *commonOpts) client() *client {
	key := *o.key
	if key == "" {
		key = os.Getenv("WAVONYX_API_KEY")
	}
	return newClient(resolveBaseURL(*o.url), key, *o.timeout)
}

// parseFlags parses args and exits with usage on error. Go's flag package stops
// at the first positional argument, so flags must precede them. Commands whose
// trailing arguments are free-form text (a message body) use this.
func parseFlags(fs *flag.FlagSet, args []string, usage string) {
	setUsage(fs, usage)
	_ = fs.Parse(args)
}

// parseFlagsAnyOrder is parseFlags for commands with a fixed number of
// positional arguments: it first moves flags ahead of them, so both
// `wavonyx messages personal -n 5` and `wavonyx messages -n 5 personal` work.
func parseFlagsAnyOrder(fs *flag.FlagSet, args []string, usage string) {
	setUsage(fs, usage)
	_ = fs.Parse(permuteFlags(fs, args))
}

func setUsage(fs *flag.FlagSet, usage string) {
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, usage)
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
	}
}

// permuteFlags reorders args so every flag comes before the positional
// arguments. Whether a flag consumes the next argument is asked of the FlagSet
// itself, the same way the flag package decides. A "--" ends flag processing.
func permuteFlags(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") { // --flag=value carries its own value
			flags = append(flags, arg)
			continue
		}
		def := fs.Lookup(name)
		if def == nil { // unknown: hand it over so Parse reports it
			flags = append(flags, arg)
			continue
		}
		if bf, ok := def.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			flags = append(flags, arg)
			continue
		}
		if i+1 < len(args) { // takes a value; keep the pair together
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		flags = append(flags, arg)
	}
	return append(flags, positional...)
}

// printJSON writes v as indented JSON.
func printJSON(v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		die(err)
	}
	fmt.Println(string(b))
}

// newTable returns a tab-aligned writer for stdout.
func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func printSessions(sessions []wavonyx.SessionInfo) {
	if len(sessions) == 0 {
		fmt.Println("No sessions yet. Create one with: wavonyx connect <id>")
		return
	}
	tw := newTable()
	fmt.Fprintln(tw, "ID\tSTATUS\tPHONE\tNAME\tCREATED")
	for _, s := range sessions {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			s.ID, s.Status, dash(s.Phone), dash(s.PushName), s.CreatedAt.Local().Format("2006-01-02 15:04"))
	}
	_ = tw.Flush()
}

func printSession(s *wavonyx.SessionInfo) {
	tw := newTable()
	fmt.Fprintf(tw, "ID\t%s\n", s.ID)
	fmt.Fprintf(tw, "Status\t%s\n", s.Status)
	fmt.Fprintf(tw, "JID\t%s\n", dash(s.JID))
	fmt.Fprintf(tw, "Phone\t%s\n", dash(s.Phone))
	fmt.Fprintf(tw, "Name\t%s\n", dash(s.PushName))
	fmt.Fprintf(tw, "Webhook\t%s\n", dash(s.WebhookURL))
	fmt.Fprintf(tw, "Created\t%s\n", s.CreatedAt.Local().Format("2006-01-02 15:04:05"))
	_ = tw.Flush()
}

func printMessages(msgs []wavonyx.InboundMessage) {
	if len(msgs) == 0 {
		fmt.Println("No messages buffered yet.")
		return
	}
	tw := newTable()
	fmt.Fprintln(tw, "TIME\tFROM\tCHAT\tMESSAGE")
	// Oldest first reads more naturally in a terminal.
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			m.Timestamp.Local().Format("15:04:05"), senderLabel(m), chatLabel(m), messageSummary(m))
	}
	_ = tw.Flush()
}

// printMessageLine prints one message as a single line, used by `watch`.
func printMessageLine(m wavonyx.InboundMessage) {
	fmt.Printf("%s  %-22s  %s\n",
		m.Timestamp.Local().Format("15:04:05"), truncate(senderLabel(m), 22), messageSummary(m))
}

// senderLabel prefers the contact's display name, falling back to their number.
func senderLabel(m wavonyx.InboundMessage) string {
	switch {
	case m.PushName != "" && m.SenderPhone != "":
		return fmt.Sprintf("%s (%s)", m.PushName, m.SenderPhone)
	case m.PushName != "":
		return m.PushName
	case m.SenderPhone != "":
		return m.SenderPhone
	default:
		return m.Sender
	}
}

func chatLabel(m wavonyx.InboundMessage) string {
	if m.IsGroup {
		return "group"
	}
	return "dm"
}

// messageSummary renders a message's content for a one-line listing, tagging
// media and noting the token needed to download it.
func messageSummary(m wavonyx.InboundMessage) string {
	text := strings.ReplaceAll(m.Text, "\n", " ")
	if m.Media == nil {
		if m.EditedID != "" {
			return "(edited) " + text
		}
		return text
	}
	label := "[" + m.Kind
	if m.Media.Filename != "" {
		label += " " + m.Media.Filename
	}
	label += "]"
	if text != "" {
		label += " " + text
	}
	return label
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
