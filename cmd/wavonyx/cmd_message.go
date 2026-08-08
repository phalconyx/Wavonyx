package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/phalconyx/wavonyx"
)

func runSend(args []string) {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	opts := registerCommon(fs)
	file := fs.String("file", "", "send this file as an attachment (the text becomes its caption)")
	parseFlags(fs, args, "Usage: wavonyx send [flags] <session-id> <to> <text...>\n\n"+
		"Send a message. With --file the remaining words become the caption.\n"+
		"Flags must come before the positional arguments.\n\n"+
		"Examples:\n"+
		"  wavonyx send personal 6281234567890 hello there\n"+
		"  wavonyx send --file invoice.pdf personal 6281234567890 your invoice")

	id, to := fs.Arg(0), fs.Arg(1)
	if id == "" || to == "" {
		die(errors.New("usage: wavonyx send <session-id> <to> <text...>"))
	}
	text := strings.Join(fs.Args()[min(2, fs.NArg()):], " ")
	if *file == "" && strings.TrimSpace(text) == "" {
		die(errors.New("nothing to send: provide message text, or --file to send an attachment"))
	}

	c := opts.client()
	ctx := context.Background()
	var (
		res *wavonyx.SendResult
		err error
	)
	if *file != "" {
		res, err = c.sendMedia(ctx, id, to, text, *file)
	} else {
		res, err = c.sendText(ctx, id, to, text)
	}
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(res)
		return
	}
	fmt.Printf("Sent to %s (id %s", res.To, res.MessageID)
	if res.TypingMS > 0 {
		fmt.Printf(", typed for %.1fs", float64(res.TypingMS)/1000)
	}
	fmt.Println(")")
}

func runMessages(args []string) {
	fs := flag.NewFlagSet("messages", flag.ExitOnError)
	opts := registerCommon(fs)
	limit := fs.Int("n", 20, "how many recent messages to show")
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx messages [flags] [session-id]\n\n"+
		"Show recent inbound messages from the server's in-memory buffer.")

	id := argOr(fs, 0, defaultSessionID)
	msgs, err := opts.client().messages(context.Background(), id, *limit)
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(msgs)
		return
	}
	printMessages(msgs)
}

// runWatch tails incoming messages by polling the server's ring buffer.
func runWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	opts := registerCommon(fs)
	interval := fs.Duration("interval", 2*time.Second, "how often to poll for new messages")
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx watch [flags] [session-id]\n\n"+
		"Print inbound messages as they arrive. Press Ctrl-C to stop.")

	id := argOr(fs, 0, defaultSessionID)
	c := opts.client()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Seed with what is already buffered so only new messages get printed.
	seen, err := seenIDs(ctx, c, id)
	if err != nil {
		die(err)
	}
	fmt.Fprintf(os.Stderr, "Watching %q for new messages… (Ctrl-C to stop)\n", id)

	ticker := time.NewTicker(*interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			msgs, err := c.messages(ctx, id, 100)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				fmt.Fprintln(os.Stderr, "warning:", err)
				continue
			}
			// Messages arrive newest-first; print the unseen ones oldest-first.
			current := make(map[string]bool, len(msgs))
			for _, m := range msgs {
				current[m.MessageID] = true
			}
			for i := len(msgs) - 1; i >= 0; i-- {
				if !seen[msgs[i].MessageID] {
					printMessageLine(msgs[i])
				}
			}
			// Tracking only what is still buffered keeps this bounded; anything
			// that aged out of the ring can never reappear.
			seen = current
		}
	}
}

func seenIDs(ctx context.Context, c *client, id string) (map[string]bool, error) {
	msgs, err := c.messages(ctx, id, 100)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		seen[m.MessageID] = true
	}
	return seen, nil
}

func runEdit(args []string) {
	fs := flag.NewFlagSet("edit", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlags(fs, args, "Usage: wavonyx edit [flags] <session-id> <to> <message-id> <new text...>\n\n"+
		"Edit a message you sent. The message id comes from the send response.")

	id, to, msgID := fs.Arg(0), fs.Arg(1), fs.Arg(2)
	if id == "" || to == "" || msgID == "" {
		die(errors.New("usage: wavonyx edit <session-id> <to> <message-id> <new text...>"))
	}
	text := strings.Join(fs.Args()[min(3, fs.NArg()):], " ")
	if strings.TrimSpace(text) == "" {
		die(errors.New("provide the new message text"))
	}

	res, err := opts.client().editMessage(context.Background(), id, to, msgID, text)
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(res)
		return
	}
	fmt.Printf("Edited %s in %s.\n", msgID, res.To)
}

func runRevoke(args []string) {
	fs := flag.NewFlagSet("revoke", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx revoke [flags] <session-id> <to> <message-id>\n\n"+
		"Delete a message you sent, for everyone.")

	id, to, msgID := fs.Arg(0), fs.Arg(1), fs.Arg(2)
	if id == "" || to == "" || msgID == "" {
		die(errors.New("usage: wavonyx revoke <session-id> <to> <message-id>"))
	}
	res, err := opts.client().revokeMessage(context.Background(), id, to, msgID)
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(res)
		return
	}
	fmt.Printf("Deleted %s in %s.\n", msgID, res.To)
}

// runMedia downloads an inbound attachment using the token from a webhook
// payload or from `wavonyx messages --json`.
func runMedia(args []string) {
	fs := flag.NewFlagSet("media", flag.ExitOnError)
	opts := registerCommon(fs)
	out := fs.String("o", "", "write to this file (default: the sender's filename, else 'download')")
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx media [flags] <session-id> <token>\n\n"+
		"Download an inbound attachment. Get the token from the webhook payload\n"+
		"or from: wavonyx messages --json <session-id>")

	id, token := fs.Arg(0), fs.Arg(1)
	if id == "" || token == "" {
		die(errors.New("usage: wavonyx media <session-id> <token>"))
	}

	data, suggested, err := opts.client().downloadMedia(context.Background(), id, token)
	if err != nil {
		die(err)
	}
	path := *out
	if path == "" {
		path = suggested
	}
	if path == "" {
		path = "download"
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		die(err)
	}
	fmt.Printf("Saved %d bytes to %s\n", len(data), path)
}
