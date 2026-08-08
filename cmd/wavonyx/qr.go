package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// ANSI colours used to draw the QR. Explicit foreground *and* background make
// the code render correctly on both light and dark terminal themes — relying on
// the theme's own colours inverts the code on half the terminals out there, and
// an inverted code is the usual reason a phone refuses to scan it.
const (
	ansiReset = "\033[0m"
	fgLight   = 97 // bright white
	fgDark    = 30 // black
	bgLight   = 47 // white
	bgDark    = 40 // black
)

// renderQR draws a QR code for content using half-block characters, so each
// terminal row carries two rows of QR modules. A WhatsApp pairing code comes out
// around 61 columns by 31 rows, which fits a standard terminal.
//
// With color=false it falls back to the encoder's own block rendering, which
// assumes a dark-background terminal; invert flips it for light backgrounds.
func renderQR(content string, color, invert bool) (string, error) {
	// Low recovery keeps the code as small as possible; a terminal is a clean,
	// undamaged "surface", so the extra redundancy buys nothing here.
	q, err := qrcode.New(content, qrcode.Low)
	if err != nil {
		return "", err
	}
	if !color {
		return q.ToSmallString(invert), nil
	}

	bitmap := q.Bitmap() // true = dark module; the 4-module quiet zone is included
	var b strings.Builder
	for y := 0; y < len(bitmap); y += 2 {
		for x := 0; x < len(bitmap[y]); x++ {
			top := bitmap[y][x]
			// Past the last row we are in the quiet zone, which is light.
			bottom := false
			if y+1 < len(bitmap) {
				bottom = bitmap[y+1][x]
			}
			if invert {
				top, bottom = !top, !bottom
			}
			// "▀" paints the top half in the foreground colour and leaves the
			// bottom half showing the background colour.
			fg, bg := fgLight, bgLight
			if top {
				fg = fgDark
			}
			if bottom {
				bg = bgDark
			}
			fmt.Fprintf(&b, "\033[%d;%dm▀", fg, bg)
		}
		b.WriteString(ansiReset + "\n")
	}
	return b.String(), nil
}

// isTTY reports whether f is an interactive terminal, so the CLI knows if it may
// use cursor movement and colour.
func isTTY(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// liveScreen redraws a block of lines in place, so a rotating QR code refreshes
// instead of scrolling the terminal. On a non-terminal writer it simply appends.
type liveScreen struct {
	w     io.Writer
	tty   bool
	lines int // height of the block currently on screen
}

func newLiveScreen(w io.Writer, tty bool) *liveScreen {
	return &liveScreen{w: w, tty: tty}
}

// render replaces the previously drawn block with text.
func (s *liveScreen) render(text string) {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if !s.tty {
		fmt.Fprintln(s.w, strings.Join(lines, "\n"))
		return
	}
	if s.lines > 0 {
		fmt.Fprintf(s.w, "\033[%dA", s.lines) // back to the top of the block
	}
	// Draw at least as many lines as last time so leftovers get cleared.
	height := len(lines)
	if s.lines > height {
		height = s.lines
	}
	for i := 0; i < height; i++ {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		fmt.Fprintf(s.w, "\r\033[K%s\n", line) // clear to end of line, then draw
	}
	s.lines = height
}
