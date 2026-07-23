// Command basic demonstrates using Wavonyx as a Go library (no HTTP server):
// it pairs a session by QR and sends one text message.
//
// Usage:
//
//	go run ./examples/basic <recipient-phone>
//	go run ./examples/basic 6281234567890
//
// On first run it prints a QR code string; scan it from WhatsApp on your phone
// (Settings > Linked Devices > Link a Device). Render the string with, e.g.:
//
//	qrencode -t ansiutf8 '<the printed code>'
//
// Credentials are saved under ./example-data, so later runs skip pairing.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/phalconyx/wavonyx"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: basic <recipient-phone, e.g. 6281234567890>")
	}
	recipient := os.Args[1]

	cfg := wavonyx.DefaultConfig()
	cfg.DataDir = "./example-data"
	mgr, err := wavonyx.NewManager(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer mgr.Close()

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		log.Fatal(err)
	}

	const id = "example"
	if _, err := mgr.Create(ctx, id, ""); err != nil && !errors.Is(err, wavonyx.ErrSessionExists) {
		log.Fatal(err)
	}

	if info, _ := mgr.Get(ctx, id); info.Status != wavonyx.StatusConnected {
		if err := pair(ctx, mgr, id); err != nil {
			log.Fatal(err)
		}
	}

	info, _ := mgr.Get(ctx, id)
	fmt.Printf("connected as %q (%s)\n", info.PushName, info.JID)

	res, err := mgr.Send(ctx, id, wavonyx.SendRequest{To: recipient, Text: "Hello from Wavonyx 👋"})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("sent: id=%s to=%s typing=%dms\n", res.MessageID, res.To, res.TypingMS)
}

// pair drives the QR pairing loop, reprinting the code whenever it rotates.
func pair(ctx context.Context, mgr *wavonyx.Manager, id string) error {
	qr, err := mgr.Login(ctx, id)
	if err != nil {
		return err
	}
	fmt.Println("Scan this in WhatsApp > Linked Devices > Link a Device:")
	fmt.Printf("  qrencode -t ansiutf8 '%s'\n", qr.Code)

	last := qr.Code
	for i := 0; i < 120; i++ {
		info, _ := mgr.Get(ctx, id)
		if info.Status == wavonyx.StatusConnected {
			return nil
		}
		if q, err := mgr.QR(ctx, id); err == nil && q.Code != last {
			last = q.Code
			fmt.Printf("QR refreshed:\n  qrencode -t ansiutf8 '%s'\n", q.Code)
		}
		time.Sleep(time.Second)
	}
	return errors.New("pairing timed out")
}
