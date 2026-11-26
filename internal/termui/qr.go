// Package termui renders terminal output for interactive agent commands:
// QR codes, a large-letter banner for pairing codes, and TTY detection.
package termui

import (
	"io"
	"os"
	"strings"

	"rsc.io/qr"
)

// QuietZone is the white border around a QR code, in modules. The QR spec
// asks for 4; scanners struggle to find the code without it.
const QuietZone = 4

// The code is drawn black on white explicitly, never with the terminal's
// default colours: on a dark theme "default foreground on default
// background" would invert the code, which many phone scanners reject.
// Basic SGR 30 (black) / 47 (white) works everywhere; 107 (bright white) is
// layered on top where supported and ignored elsewhere.
const (
	qrColors = "\x1b[30;47m\x1b[107m"
	qrReset  = "\x1b[0m"
)

// Matrix is a square QR module grid; true means a dark module.
type Matrix interface {
	Size() int
	Black(x, y int) bool
}

type qrMatrix struct{ c *qr.Code }

func (m qrMatrix) Size() int           { return m.c.Size }
func (m qrMatrix) Black(x, y int) bool { return m.c.Black(x, y) }

// EncodeQR encodes text at error-correction level L (a terminal is a clean,
// high-contrast surface, and L keeps the code small enough for narrow
// windows).
func EncodeQR(text string) (Matrix, error) {
	c, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, err
	}
	return qrMatrix{c}, nil
}

// RenderQR writes text as a QR code drawn with Unicode half blocks: every
// character cell holds two vertically stacked modules, so modules come out
// roughly square in a terminal font. Each line sets its own colours and
// resets them at the end, so nothing bleeds into the rest of the terminal.
func RenderQR(w io.Writer, text string) error {
	m, err := EncodeQR(text)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, RenderMatrix(m, "  "))
	return err
}

// RenderMatrix renders m with a quiet zone; indent is printed (in the
// terminal's own colours) before every line.
func RenderMatrix(m Matrix, indent string) string {
	n := m.Size()
	total := n + 2*QuietZone
	dark := func(x, y int) bool {
		x, y = x-QuietZone, y-QuietZone
		return x >= 0 && y >= 0 && x < n && y < n && m.Black(x, y)
	}
	var b strings.Builder
	for y := 0; y < total; y += 2 {
		b.WriteString(indent)
		b.WriteString(qrColors)
		for x := 0; x < total; x++ {
			top, bottom := dark(x, y), y+1 < total && dark(x, y+1)
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		b.WriteString(qrReset)
		b.WriteByte('\n')
	}
	return b.String()
}

// QRWidth returns the width in terminal columns of text's QR code
// (including the quiet zone), or 0 if it cannot be encoded.
func QRWidth(text string) int {
	m, err := EncodeQR(text)
	if err != nil {
		return 0
	}
	return m.Size() + 2*QuietZone
}

// IsTerminal reports whether f is an interactive terminal (a character
// device) and TERM does not declare a dumb terminal.
func IsTerminal(f *os.File) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
