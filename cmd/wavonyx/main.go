// Command wavonyx runs the Wavonyx WhatsApp gateway and drives it from the
// terminal.
//
// The `serve` command runs the gateway itself; every other command is a thin
// HTTP client for a running server. WhatsApp sessions are long-lived WebSocket
// connections owned by that server, so the CLI never opens one itself — this
// also means `wavonyx connect` can be run from anywhere that can reach the
// server, including the host of a container running it.
//
// Server configuration comes from WAVONYX_* environment variables; the client
// commands read WAVONYX_URL, WAVONYX_ADDR, and WAVONYX_API_KEY (see the help
// output or .env.example).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/phalconyx/wavonyx"
	"github.com/phalconyx/wavonyx/server"
	"github.com/phalconyx/wavonyx/typing"
)

const version = "0.1.0"

const usage = `Wavonyx - WhatsApp send/receive gateway

Server:
  wavonyx serve                              Run the HTTP server (default :9900)
  wavonyx healthcheck                        Probe the local server's /health

Sessions (need a running server):
  wavonyx connect [id]                       Pair an account — shows a QR code to scan
  wavonyx list                               List sessions and their status
  wavonyx status [id]                        Show one session in detail
  wavonyx webhook <id> [url]                 Show or set this session's webhook
  wavonyx logout [id]                        Unlink the device, keep the session
  wavonyx delete <id>                        Delete the session and its credentials

Messages:
  wavonyx send <id> <to> <text...>           Send a text message
  wavonyx send --file f.jpg <id> <to> [cap]  Send an attachment
  wavonyx messages [id] [-n 20]              Show recent inbound messages
  wavonyx watch [id]                         Print inbound messages as they arrive
  wavonyx edit <id> <to> <msg-id> <text...>  Edit a message you sent
  wavonyx revoke <id> <to> <msg-id>          Delete a message you sent, for everyone
  wavonyx media <id> <token> [-o file]       Download an inbound attachment

Help:
  wavonyx help                               Show this message
  wavonyx help <command>                     Help for one command
  wavonyx version                            Print version

'wavonyx help <command>' and 'wavonyx <command> -h' are the same thing. Client
commands accept --url, --key, --json, and --timeout, either before or after the
command; other flags go before the positional arguments.

Client environment:
  WAVONYX_URL                     Server URL, e.g. https://wa.example.com
                                  (falls back to WAVONYX_ADDR, then http://127.0.0.1:9900)
  WAVONYX_API_KEY                 API key sent as X-API-Key

Server environment:
  WAVONYX_ADDR                    Listen address (default ":9900")
  WAVONYX_API_KEY                 Require this key via X-API-Key (empty = auth off)
  WAVONYX_DATA_DIR                SQLite data directory (default "./data")
  WAVONYX_DEVICE_NAME             Name in WhatsApp Linked Devices (default "Wavonyx")

  WAVONYX_WEBHOOK_URL             Global webhook endpoint for inbound messages
  WAVONYX_WEBHOOK_SECRET          HMAC-SHA256 signing key (empty = unsigned)
  WAVONYX_WEBHOOK_TIMEOUT         Per-attempt timeout (default 10s)
  WAVONYX_WEBHOOK_RETRIES         Delivery attempts (default 5)
  WAVONYX_WEBHOOK_WORKERS         Delivery workers, 1 = ordered (default 1)
  WAVONYX_WEBHOOK_INCLUDE_FROM_ME Deliver own-device messages (default false)

  WAVONYX_TYPING_MODE             off | constant | natural (default natural)
  WAVONYX_TYPING_PER_CHAR         Per-rune delay in constant mode (default 55ms)
  WAVONYX_TYPING_MIN_CPS          Natural mode min chars/sec (default 4)
  WAVONYX_TYPING_MAX_CPS          Natural mode max chars/sec (default 9)
  WAVONYX_TYPING_MIN_TOTAL        Typing floor (default 400ms)
  WAVONYX_TYPING_MAX_TOTAL        Typing ceiling / stall guard (default 15s)

  WAVONYX_SEND_MIN_GAP            Pause between sends per session (default 1s)
  WAVONYX_SEND_QUEUE              Per-session send queue depth (default 100)
  WAVONYX_RING_SIZE              Per-session inbound buffer (default 200)
  WAVONYX_MAX_MEDIA_BYTES        Max outbound media size, e.g. 64MB (default 64MB)
  WAVONYX_LOG_LEVEL              debug | info | warn | error (default info)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
	command, args := splitCommand(os.Args[1:])
	dispatch(command, args)
}

func dispatch(command string, args []string) {
	switch command {
	case "serve":
		runServe()
	case "healthcheck":
		runHealthcheck()
	case "connect", "login":
		runConnect(args)
	case "list", "ls", "sessions":
		runList(args)
	case "status":
		runStatus(args)
	case "webhook":
		runWebhook(args)
	case "logout":
		runLogout(args)
	case "delete", "rm":
		runDelete(args)
	case "send":
		runSend(args)
	case "messages", "msgs":
		runMessages(args)
	case "watch", "tail":
		runWatch(args)
	case "edit":
		runEdit(args)
	case "revoke":
		runRevoke(args)
	case "media", "download":
		runMedia(args)
	case "version", "-v", "--version":
		fmt.Println("wavonyx", version)
	case "help", "-h", "--help":
		runHelp(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", command)
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}
}

// runHelp prints the overall usage, or the help for one command:
//
//	wavonyx help            the command summary
//	wavonyx help connect    the same as: wavonyx connect -h
func runHelp(args []string) {
	if len(args) == 0 {
		fmt.Print(usage)
		return
	}
	switch args[0] {
	case "serve":
		fmt.Println("Usage: wavonyx serve\n\n" +
			"Run the WhatsApp gateway. Configured entirely through WAVONYX_* environment\n" +
			"variables — see 'wavonyx help' for the list.")
	case "healthcheck":
		fmt.Println("Usage: wavonyx healthcheck\n\n" +
			"Probe the local server's /health endpoint, exiting non-zero when it is\n" +
			"unreachable or unhealthy. Used as the container healthcheck, because the\n" +
			"distroless image ships no shell or curl.")
	case "version", "help":
		fmt.Print(usage)
	default:
		// Every other command owns a FlagSet, which prints its own help for -h.
		dispatch(args[0], []string{"-h"})
	}
}

// Global flags accepted before the subcommand, so both spellings work:
//
//	wavonyx --url http://host list
//	wavonyx list --url http://host
var (
	globalValueFlags = map[string]bool{"url": true, "key": true, "timeout": true}
	globalBoolFlags  = map[string]bool{"json": true}
)

// splitCommand pulls any leading global flags off args and moves them in front
// of the subcommand's own arguments, where its FlagSet will parse them.
func splitCommand(args []string) (command string, rest []string) {
	var leading []string
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "-" {
		name := strings.TrimLeft(args[i], "-")
		inline := strings.Contains(name, "=") // --flag=value carries its own value
		name, _, _ = strings.Cut(name, "=")
		switch {
		case inline, globalBoolFlags[name]:
			leading = append(leading, args[i])
			i++
		case globalValueFlags[name] && i+1 < len(args):
			leading = append(leading, args[i], args[i+1])
			i += 2
		default:
			// Not a global flag (e.g. -h): let the normal dispatch handle it.
			return args[i], append(leading, args[i+1:]...)
		}
	}
	if i >= len(args) {
		// Only flags were given, with no command.
		return "", leading
	}
	return args[i], append(leading, args[i+1:]...)
}

type serveConfig struct {
	cfg    wavonyx.Config
	addr   string
	apiKey string
}

func runServe() {
	sc, err := configFromEnv()
	if err != nil {
		die(err)
	}
	level := parseLogLevel(getenv("WAVONYX_LOG_LEVEL", "info"))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	sc.cfg.Logger = logger

	mgr, err := wavonyx.NewManager(sc.cfg)
	if err != nil {
		die(err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		_ = mgr.Close()
		die(err)
	}

	srv := &http.Server{
		Addr:              sc.addr,
		Handler:           server.New(mgr, server.Config{APIKey: sc.apiKey, MaxUploadBytes: sc.cfg.MaxMediaBytes}),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("wavonyx listening", "addr", sc.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
	case err := <-errCh:
		if err != nil {
			_ = mgr.Close()
			die(err)
		}
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown", "err", err)
	}
	if err := mgr.Close(); err != nil {
		logger.Error("manager close", "err", err)
	}
}

func configFromEnv() (serveConfig, error) {
	sc := serveConfig{
		cfg:    wavonyx.DefaultConfig(),
		addr:   getenv("WAVONYX_ADDR", ":9900"),
		apiKey: os.Getenv("WAVONYX_API_KEY"),
	}
	cfg := &sc.cfg

	if v := os.Getenv("WAVONYX_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("WAVONYX_DEVICE_NAME"); v != "" {
		cfg.DeviceName = v
	}

	if v := os.Getenv("WAVONYX_TYPING_MODE"); v != "" {
		cfg.Typing.Mode = typing.Mode(v)
	}
	if err := setDuration(&cfg.Typing.PerChar, "WAVONYX_TYPING_PER_CHAR"); err != nil {
		return sc, err
	}
	if err := setFloat(&cfg.Typing.MinCPS, "WAVONYX_TYPING_MIN_CPS"); err != nil {
		return sc, err
	}
	if err := setFloat(&cfg.Typing.MaxCPS, "WAVONYX_TYPING_MAX_CPS"); err != nil {
		return sc, err
	}
	if err := setDuration(&cfg.Typing.MinTotal, "WAVONYX_TYPING_MIN_TOTAL"); err != nil {
		return sc, err
	}
	if err := setDuration(&cfg.Typing.MaxTotal, "WAVONYX_TYPING_MAX_TOTAL"); err != nil {
		return sc, err
	}

	if err := setDuration(&cfg.SendMinGap, "WAVONYX_SEND_MIN_GAP"); err != nil {
		return sc, err
	}
	if err := setInt(&cfg.SendQueue, "WAVONYX_SEND_QUEUE"); err != nil {
		return sc, err
	}
	if err := setInt(&cfg.RingSize, "WAVONYX_RING_SIZE"); err != nil {
		return sc, err
	}
	if err := setSize(&cfg.MaxMediaBytes, "WAVONYX_MAX_MEDIA_BYTES"); err != nil {
		return sc, err
	}
	cfg.IncludeFromMe = envBool("WAVONYX_WEBHOOK_INCLUDE_FROM_ME")

	cfg.Webhook.URL = os.Getenv("WAVONYX_WEBHOOK_URL")
	cfg.Webhook.Secret = os.Getenv("WAVONYX_WEBHOOK_SECRET")
	if err := setDuration(&cfg.Webhook.Timeout, "WAVONYX_WEBHOOK_TIMEOUT"); err != nil {
		return sc, err
	}
	if err := setInt(&cfg.Webhook.Retries, "WAVONYX_WEBHOOK_RETRIES"); err != nil {
		return sc, err
	}
	if err := setInt(&cfg.Webhook.Workers, "WAVONYX_WEBHOOK_WORKERS"); err != nil {
		return sc, err
	}
	return sc, nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func setDuration(dst *time.Duration, key string) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	*dst = d
	return nil
}

func setInt(dst *int, key string) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	*dst = n
	return nil
}

func setFloat(dst *float64, key string) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	*dst = f
	return nil
}

func setSize(dst *int64, key string) error {
	v := os.Getenv(key)
	if v == "" {
		return nil
	}
	n, err := parseSize(v)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", key, err)
	}
	*dst = n
	return nil
}

// parseSize parses a byte size with an optional B/KB/MB/GB suffix.
func parseSize(s string) (int64, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "GB"):
		mult, s = 1<<30, strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		mult, s = 1<<20, strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		mult, s = 1<<10, strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, err
	}
	return n * mult, nil
}

func envBool(key string) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// runHealthcheck probes the local server's /health endpoint and exits non-zero
// when it is unreachable or unhealthy. It exists because the distroless runtime
// image has no shell or curl for container healthchecks.
func runHealthcheck() {
	addr := getenv("WAVONYX_ADDR", ":9900")
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		die(fmt.Errorf("invalid WAVONYX_ADDR %q: %w", addr, err))
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/health")
	if err != nil {
		die(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		die(fmt.Errorf("health endpoint returned status %d", resp.StatusCode))
	}
	fmt.Println("ok")
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "unauthorized" {
		fmt.Fprintln(os.Stderr, "\nThis server requires an API key. Either export it once:")
		fmt.Fprintln(os.Stderr, "  export WAVONYX_API_KEY=<key>")
		fmt.Fprintln(os.Stderr, "or pass it per command:")
		fmt.Fprintln(os.Stderr, "  wavonyx --key <key> <command>")
	}
	os.Exit(1)
}
