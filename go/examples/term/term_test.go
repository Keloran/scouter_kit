//go:build emulator

package main

import (
	"encoding/binary"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func start(t *testing.T, c config) *term {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	tm, err := newTerm(cfg, demo.Present(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tm.close)
	return tm
}

// save writes the emulated panels as PNGs when G1_PNG names a directory.
func save(t *testing.T, name string) {
	dir := os.Getenv("G1_PNG")
	if dir == "" {
		return
	}
	for _, arm := range demo.Present() {
		f, err := os.Create(filepath.Join(dir, name+"-"+arm.String()+".png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, arm.EmulatedPanel())
		f.Close()
	}
}

// The -check screens, at both font scales: each read back on both panels.
func TestCheck(t *testing.T) {
	for _, scale := range []int{1, 2} {
		t.Run(string(rune('0'+scale)), func(t *testing.T) {
			tm := start(t, config{scale: scale})
			if err := tm.check(); err != nil {
				t.Fatal(err)
			}
			save(t, "term-check"+string(rune('0'+scale)))
			t.Logf("%d x %d cells of %d x %d px; %d frames", tm.g.cols, tm.g.rows, tm.g.cw, tm.g.ch, len(tm.shown))
		})
	}
}

// Every glyph of the terminal's font (all of it, as embedded) has the
// cell's advance and stays inside its cell, so a row never draws into the
// next slot.
func TestGlyphsInCells(t *testing.T) {
	f, err := demo.ParseFont(nerdFont)
	if err != nil {
		t.Fatal(err)
	}
	n := int(binary.LittleEndian.Uint16(nerdFont[8:]))
	cw, ch := 7, f.LineSpacing
	for i := 0; i < n; i++ {
		img := demo.NewImage(3*cw, 3*ch)
		if adv := f.Draw(img, cw, ch+f.Ascent, []uint32{uint32(i)}, 15); adv != cw && adv != 0 {
			t.Fatalf("glyph %d: advance %d", i, adv)
		}
		for y := 0; y < img.H; y++ {
			for x := 0; x < img.W; x++ {
				if img.At(x, y) != 0 && (y < ch || y >= 2*ch || x < cw || x >= 2*cw) {
					t.Fatalf("glyph %d has ink outside its cell at %d,%d", i, x-cw, y-ch)
				}
			}
		}
	}
	if n < 10000 || !codepoints(nerdFont)[0x28ff] || !codepoints(nerdFont)[0xf07b] {
		t.Fatalf("%d glyphs: not the Nerd Font with braille", n)
	}
}

// Braille, block elements and powerline glyphs are on the arms from the
// start; an icon goes there when the screen first shows it (the font is
// loaded again and the screen redrawn); a character the font has not shows
// as "?" without loading again. Both panels read back each time.
func TestNerdGlyphs(t *testing.T) {
	tm := start(t, config{scale: 1})
	for r := rune(0x2800); r <= 0x28ff; r++ {
		if !tm.loaded[r] {
			t.Fatalf("U+%04X not loaded at the start", r)
		}
	}
	shown := func(s string) {
		t.Helper()
		tm.feed(s)
		if err := tm.update(); err != nil {
			t.Fatal(err)
		}
		if err := tm.checkPanels(); err != nil {
			t.Fatal(err)
		}
	}
	shown("\x1b[2J\x1b[H\u28ff\u2809\u28c0 \u2589\u259a\u2591 \ue0b0\ue0a0 \u256d\u2500\u256e")
	if tm.loaded[0xf07b] {
		t.Fatal("an icon loaded before it was shown")
	}
	shown("\r\n\uf07b folder \uf113 github \ue73c python \u65e5")
	for _, r := range []rune{0xf07b, 0xf113, 0xe73c} {
		if !tm.loaded[r] || tm.glyph(r) == tm.unknown {
			t.Fatalf("U+%04X not loaded on demand", r)
		}
	}
	if tm.glyph(0x65e5) != tm.unknown {
		t.Fatal("a character the font has not")
	}
	frames := len(tm.shown)
	if err := tm.update(); err != nil || len(tm.shown) != frames {
		t.Fatalf("a frame without a change (%v)", err)
	}
	save(t, "term-nerd")
	t.Logf("%d glyphs on the arms, %d bytes free", len(tm.loaded), scouterkit.FreeMemory())
}

// btop in the wide mode (it needs 80 x 24, more than the default 77
// columns): every character it shows (braille graphs, line drawing) has its
// glyph, and both panels read back.
func TestBtop(t *testing.T) {
	if _, err := exec.LookPath("btop"); err != nil {
		t.Skip("btop not installed")
	}
	tm := start(t, config{scale: 1, wide: true})
	tm.feed(string(record(t, tm.g.cols, tm.g.rows, []string{"", ""}, "btop")))
	if err := tm.update(); err != nil {
		t.Fatal(err)
	}
	if err := tm.checkPanels(); err != nil {
		t.Fatal(err)
	}
	seen := map[rune]bool{}
	for _, row := range tm.vt.grid {
		for _, c := range row {
			if c.r > 0x7e && !seen[c.r] {
				seen[c.r] = true
				if tm.glyph(c.r) == tm.unknown {
					t.Errorf("U+%04X %c has no glyph", c.r, c.r)
				}
			}
		}
	}
	if len(seen) < 10 {
		t.Fatalf("only %d characters beyond ASCII: not btop's screen", len(seen))
	}
	save(t, "term-btop")
	t.Logf("%d characters beyond ASCII, all with glyphs", len(seen))
}

// A command on the pseudo-terminal: its output scrolls the screen, which
// both panels show; it gets its window size and TERM.
func TestCommand(t *testing.T) {
	tm := start(t, config{scale: 1, cols: 60, rows: 20})
	script := `i=1; while [ $i -le 45 ]; do echo "line $i"; i=$((i+1)); done; ` +
		`printf '\033[1;32mdone %s %s\033[m' "$(stty size)" "$TERM"`
	if err := tm.run([]string{"/bin/sh", "-c", script}); err != nil {
		t.Fatal(err)
	}
	if err := tm.checkPanels(); err != nil {
		t.Fatal(err)
	}
	save(t, "term-command")
	got := strings.Join(text(tm.vt), "\n")
	if !strings.Contains(got, "line 45\ndone 20 60 xterm-256color") || strings.Contains(got, "line 25\n") {
		t.Fatalf("screen:\n%s", got)
	}
	if err := demo.Halted(tm.arms); err != nil {
		t.Fatal(err)
	}
}

// The wide mode: the whole virtual display, each eye its 640-px window of
// it, every screen read back on both panels; the first column only the left
// eye shows, the last only the right eye.
func TestWide(t *testing.T) {
	for _, c := range []struct{ scale, cols, rows int }{{1, 105, 33}, {2, 52, 16}} {
		t.Run(strconv.Itoa(c.scale), func(t *testing.T) {
			tm := start(t, config{scale: c.scale, wide: true})
			if tm.g.cols != c.cols || tm.g.rows != c.rows {
				t.Fatalf("%d x %d cells", tm.g.cols, tm.g.rows)
			}
			if err := tm.check(); err != nil {
				t.Fatal(err)
			}
			tm.feed("\x1b[2J\x1b[HL" + strings.Repeat(".", tm.g.cols-2) + "R")
			if err := tm.update(); err != nil {
				t.Fatal(err)
			}
			if err := tm.checkPanels(); err != nil {
				t.Fatal(err)
			}
			save(t, "term-wide"+strconv.Itoa(c.scale))
			ink := func(arm scouterkit.Arm, col int) bool {
				panel, p := tm.expected(arm), tm.pos[arm]
				for y := p[1]; y < p[1]+tm.g.ch; y++ {
					for x := p[0] + col*tm.g.cw; x < p[0]+(col+1)*tm.g.cw; x++ {
						if panel.At(x, y) != 0 {
							return true
						}
					}
				}
				return false
			}
			l, r, last := scouterkit.Left, scouterkit.Right, tm.g.cols-1
			if !ink(l, 0) || ink(r, 0) || ink(l, last) || !ink(r, last) {
				t.Fatalf("column 0 on the left/right eye %v/%v, column %d %v/%v", ink(l, 0), ink(r, 0), last,
					ink(l, last), ink(r, last))
			}
			t.Logf("%d x %d cells at x %d (left eye) and %d (right eye)", tm.g.cols, tm.g.rows, tm.pos[l][0], tm.pos[r][0])
		})
	}
}
