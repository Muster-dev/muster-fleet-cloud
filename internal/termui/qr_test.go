package termui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from golden output (run go test -update after checking):\ngot:\n%s\nwant:\n%s", name, got, want)
	}
}

const pairURL = "https://cloud.getmuster.dev/pair?code=KX7M-42QP"

func TestRenderQRGolden(t *testing.T) {
	var b strings.Builder
	if err := RenderQR(&b, pairURL); err != nil {
		t.Fatal(err)
	}
	golden(t, "qr_pair_url.golden", b.String())
}

// fake is a tiny matrix for structural checks independent of the encoder.
type fake struct {
	n    int
	dark map[[2]int]bool
}

func (f fake) Size() int           { return f.n }
func (f fake) Black(x, y int) bool { return f.dark[[2]int{x, y}] }

func TestRenderMatrixHalfBlocksAndQuietZone(t *testing.T) {
	// 3x3: (0,0) top-only in its cell pair, (1,0)+(1,1) full, (2,1) bottom-only.
	m := fake{n: 3, dark: map[[2]int]bool{{0, 0}: true, {1, 0}: true, {1, 1}: true, {2, 1}: true, {0, 2}: true}}
	out := RenderMatrix(m, "> ")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	total := 3 + 2*QuietZone // 11 modules → 6 text rows (last half is padding)
	if len(lines) != (total+1)/2 {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), (total+1)/2, out)
	}
	for i, l := range lines {
		if !strings.HasPrefix(l, "> "+qrColors) || !strings.HasSuffix(l, qrReset) {
			t.Fatalf("line %d lacks indent/colours/reset: %q", i, l)
		}
		body := []rune(strings.TrimSuffix(strings.TrimPrefix(l, "> "+qrColors), qrReset))
		if len(body) != total {
			t.Fatalf("line %d is %d cells wide, want %d", i, len(body), total)
		}
		// Quiet zone: the first and last 4 columns, and the first/last two
		// rows, are blank (white background).
		for x := 0; x < QuietZone; x++ {
			if body[x] != ' ' || body[total-1-x] != ' ' {
				t.Fatalf("quiet zone not blank on line %d: %q", i, string(body))
			}
		}
		if (i < QuietZone/2 || i >= len(lines)-QuietZone/2) && strings.TrimSpace(string(body)) != "" {
			t.Fatalf("quiet zone row %d not blank: %q", i, string(body))
		}
	}
	// Modules start at y=4 → text row 2 holds module rows 0 and 1.
	row := []rune(strings.TrimSuffix(strings.TrimPrefix(lines[2], "> "+qrColors), qrReset))
	if got := string(row[QuietZone : QuietZone+3]); got != "▀█▄" {
		t.Fatalf("half blocks: got %q, want %q", got, "▀█▄")
	}
	row = []rune(strings.TrimSuffix(strings.TrimPrefix(lines[3], "> "+qrColors), qrReset))
	if got := string(row[QuietZone : QuietZone+3]); got != "▀  " {
		t.Fatalf("odd last row: got %q", got)
	}
}

func TestQRWidthFitsCommonTerminals(t *testing.T) {
	if w := QRWidth(pairURL); w == 0 || w > 45 {
		t.Fatalf("QR for the pairing URL is %d columns wide", w)
	}
}

func TestBigTextGolden(t *testing.T) {
	out := BigText("KX7M-42QP", "  ")
	if strings.Count(out, "\n") != 5 {
		t.Fatalf("want 5 lines:\n%s", out)
	}
	golden(t, "bigtext.golden", out)
	for _, r := range "ABCDEFGHJKLMNPQRSTUVWXYZ23456789-" {
		if _, ok := glyphs[r]; !ok {
			t.Errorf("no glyph for %q", r)
		}
	}
}
