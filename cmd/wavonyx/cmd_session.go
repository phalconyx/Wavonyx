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

// defaultSessionID is used when a command that needs a session gets no id.
const defaultSessionID = "default"

func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx list [flags]\n\nList sessions and their connection status.")

	sessions, err := opts.client().listSessions(context.Background())
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(sessions)
		return
	}
	printSessions(sessions)
}

func runStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx status [flags] [session-id]\n\nShow one session in detail.")

	id := argOr(fs, 0, defaultSessionID)
	info, err := opts.client().getSession(context.Background(), id)
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(info)
		return
	}
	printSession(info)
}

// runWebhook shows or changes a session's own webhook URL. Each session can
// point at a different endpoint; an empty one falls back to the server's global
// WAVONYX_WEBHOOK_URL.
func runWebhook(args []string) {
	fs := flag.NewFlagSet("webhook", flag.ExitOnError)
	opts := registerCommon(fs)
	clear := fs.Bool("clear", false, "remove the session's webhook, falling back to the global one")
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx webhook [flags] <session-id> [url]\n\n"+
		"Show or set the webhook URL for one session.\n\n"+
		"Examples:\n"+
		"  wavonyx webhook toko                              show the current URL\n"+
		"  wavonyx webhook toko https://app.example/wh/toko  set it\n"+
		"  wavonyx webhook toko --clear                      use the global webhook")

	id := fs.Arg(0)
	if id == "" {
		die(errors.New("usage: wavonyx webhook <session-id> [url]"))
	}
	target, setting := fs.Arg(1), *clear
	if target != "" {
		setting = true
	}
	if *clear {
		target = ""
	}

	c := opts.client()
	ctx := context.Background()

	var (
		info *wavonyx.SessionInfo
		err  error
	)
	if setting {
		info, err = c.updateWebhook(ctx, id, target)
	} else {
		info, err = c.getSession(ctx, id)
	}
	if err != nil {
		die(err)
	}
	if *opts.json {
		printJSON(info)
		return
	}
	switch {
	case !setting:
		if info.WebhookURL == "" {
			fmt.Printf("Session %q has no webhook of its own; it uses the server's global one.\n", id)
			return
		}
		fmt.Printf("Session %q delivers to %s\n", id, info.WebhookURL)
	case info.WebhookURL == "":
		fmt.Printf("Cleared the webhook for %q; it now uses the server's global one.\n", id)
	default:
		fmt.Printf("Session %q now delivers to %s\n", id, info.WebhookURL)
	}
}

func runLogout(args []string) {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx logout [flags] [session-id]\n\nUnlink the device from WhatsApp, keeping the session.")

	id := argOr(fs, 0, defaultSessionID)
	if err := opts.client().logout(context.Background(), id); err != nil {
		die(err)
	}
	fmt.Printf("Session %q logged out. Pair again with: wavonyx connect %s\n", id, id)
}

func runDelete(args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	opts := registerCommon(fs)
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx delete [flags] <session-id>\n\nDelete a session and its stored WhatsApp credentials.")

	id := fs.Arg(0)
	if id == "" {
		die(errors.New("usage: wavonyx delete <session-id>"))
	}
	if err := opts.client().deleteSession(context.Background(), id); err != nil {
		die(err)
	}
	fmt.Printf("Session %q deleted.\n", id)
}

// runConnect creates the session if needed, then shows a live QR code in the
// terminal until the pairing is scanned. The session itself lives on in the
// server after this command exits.
func runConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	opts := registerCommon(fs)
	webhook := fs.String("webhook", "", "per-session webhook URL (only used when creating the session)")
	noColor := fs.Bool("no-color", false, "draw the QR without ANSI colours")
	invert := fs.Bool("invert", false, "invert the QR (try this if your scanner won't read it)")
	wait := fs.Duration("wait", 2*time.Minute, "how long to wait for the scan")
	parseFlagsAnyOrder(fs, args, "Usage: wavonyx connect [flags] [session-id]\n\n"+
		"Pair a WhatsApp account by scanning a QR code shown in this terminal.\n"+
		"Requires a running server (wavonyx serve).")

	id := argOr(fs, 0, defaultSessionID)
	c := opts.client()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Create on demand; an existing session is fine.
	if _, err := c.createSession(ctx, id, *webhook); err != nil && !codeIs(err, "session_exists") {
		die(err)
	}

	info, err := c.getSession(ctx, id)
	if err != nil {
		die(err)
	}
	if info.Status == wavonyx.StatusConnected {
		fmt.Printf("Session %q is already connected as %s (%s).\n", id, dash(info.PushName), dash(info.Phone))
		return
	}

	qr, err := c.login(ctx, id)
	if err != nil {
		die(err)
	}
	if qr == nil {
		die(errors.New("server did not return a QR code"))
	}

	tty := isTTY(os.Stdout)
	screen := newLiveScreen(os.Stdout, tty)
	useColor := tty && !*noColor

	draw := func(code string) {
		art, err := renderQR(code, useColor, *invert)
		if err != nil {
			die(fmt.Errorf("render QR: %w", err))
		}
		screen.render(fmt.Sprintf(
			"Session %q — scan this with WhatsApp on your phone:\n"+
				"  WhatsApp › Settings › Linked Devices › Link a Device\n\n%s\n"+
				"Waiting for the scan… the code refreshes by itself. Press Ctrl-C to cancel.",
			id, art))
	}
	draw(qr.Code)

	lastCode := qr.Code
	deadline := time.After(*wait)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Println("\nCancelled. The session stays unpaired; run connect again when ready.")
			return

		case <-deadline:
			die(fmt.Errorf("timed out after %s waiting for the QR to be scanned", *wait))

		case <-ticker.C:
			info, err := c.getSession(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return // interrupted mid-request
				}
				die(err)
			}
			switch info.Status {
			case wavonyx.StatusConnected:
				screen.render(fmt.Sprintf("✓ Session %q connected as %s (%s).", id, dash(info.PushName), dash(info.Phone)))
				if *opts.json {
					printJSON(info)
				}
				return
			case wavonyx.StatusPairing:
				// Pick up the rotated code, if any.
				if next, err := c.qr(ctx, id); err == nil && next != nil && next.Code != lastCode {
					lastCode = next.Code
					draw(next.Code)
				}
			default:
				// Server gave up on the pairing (code expired without a scan).
				die(fmt.Errorf("pairing ended with status %q — run 'wavonyx connect %s' to try again", info.Status, id))
			}
		}
	}
}

// argOr returns the nth positional argument, or def when absent.
func argOr(fs *flag.FlagSet, n int, def string) string {
	if v := strings.TrimSpace(fs.Arg(n)); v != "" {
		return v
	}
	return def
}
