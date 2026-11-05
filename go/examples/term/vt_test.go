package main

import (
	"strings"
	"testing"
)

func text(t *vt) []string {
	var out []string
	for _, row := range t.grid {
		var b strings.Builder
		for _, c := range row {
			if c.r != 0 {
				b.WriteRune(c.r)
			}
		}
		out = append(out, strings.TrimRight(b.String(), " "))
	}
	return out
}

func screenIs(t *testing.T, v *vt, want ...string) {
	t.Helper()
	got := text(v)
	for len(want) < len(got) {
		want = append(want, "")
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d: %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPrintWrapScroll(t *testing.T) {
	v := newVT(5, 3)
	v.Write([]byte("12345"))
	if v.x != 4 || !v.wrapNext {
		t.Fatalf("after the last column: x %d, wrapNext %v", v.x, v.wrapNext)
	}
	v.Write([]byte("6\r\nab\tc\r\nxyz\r\nend"))
	screenIs(t, v, "ab  c", "xyz", "end")
	if v.scrolled != 2 {
		t.Errorf("scrolled %d", v.scrolled)
	}
	v.Write([]byte("\x1b[?7l\x1b[H123456789"))
	screenIs(t, v, "12349", "xyz", "end")
}

func TestCursorAndErase(t *testing.T) {
	v := newVT(10, 4)
	v.Write([]byte("aaaaaaaaaa\r\nbbbbbbbbbb\r\ncccccccccc\r\ndddddddddd"))
	v.Write([]byte("\x1b[2;3H\x1b[K\x1b[3;5H\x1b[1K\x1b[4;1H\x1b[2K\x1b[1;4H\x1b[2X\x1b[1;9H\x1b[P"))
	screenIs(t, v, "aaa  aaaa", "bb", "     ccccc")
	v.Write([]byte("\x1b[1;1H\x1b[2@xy\x1b[1;1H\x1b[J"))
	screenIs(t, v)
	v.Write([]byte("one\x1b[3Dtwo\x1b[Bthree\x1b[10;10H!"))
	screenIs(t, v, "two", "   three", "", "         !")
	v.Write([]byte("\x1b[2;2H\x1b[6n"))
	var reply string
	v.reply = func(b []byte) { reply += string(b) }
	v.Write([]byte("\x1b[6n\x1b[c\x1b[>c"))
	if reply != "\x1b[2;2R\x1b[?62;22c\x1b[>0;0;0c" {
		t.Errorf("replies %q", reply)
	}
}

func TestRegionsAndLines(t *testing.T) {
	v := newVT(6, 5)
	v.Write([]byte("top\r\n1\r\n2\r\n3\r\nbottom"))
	v.Write([]byte("\x1b[2;4r\x1b[4;1H\nnew"))
	screenIs(t, v, "top", "2", "3", "new", "bottom")
	if v.scrolled != 0 {
		t.Errorf("a region's scroll counted as the whole screen's")
	}
	v.Write([]byte("\x1b[2;1H\x1b[L\x1b[3;1H\x1b[2M"))
	screenIs(t, v, "top", "", "", "", "bottom")
	v.Write([]byte("\x1b[2;1Hx\x1bM\x1bMy"))
	screenIs(t, v, "top", " y", "", "x", "bottom")
	v.Write([]byte("\x1b[r\x1b[5;1H\x1b[2S"))
	screenIs(t, v, "", "x", "bottom")
	v.Write([]byte("\x1b[T"))
	screenIs(t, v, "", "", "x", "bottom")
}

func TestStyles(t *testing.T) {
	v := newVT(20, 2)
	v.Write([]byte("\x1b[1;31ma\x1b[0;38;5;208mb\x1b[38;2;1;2;3mc\x1b[38:2::4:5:6md\x1b[7;48;5;4me\x1b[27;49;4mf\x1b[mg"))
	want := []style{
		{fg: 2, attr: attrBold}, {fg: 209}, {fg: direct | 0x010203}, {fg: direct | 0x040506},
		{fg: direct | 0x040506, bg: 5, attr: attrReverse}, {fg: direct | 0x040506, attr: attrUnderline}, {},
	}
	for i, w := range want {
		if got := v.grid[0][i].st; got != w {
			t.Errorf("cell %d: %+v, want %+v", i, got, w)
		}
	}
	v.Write([]byte("\x1b[44m\x1b[2;1H\x1b[K"))
	if v.grid[1][19].st.bg != 5 {
		t.Errorf("erase does not use the background")
	}
}

func TestAlternateScreen(t *testing.T) {
	v := newVT(8, 3)
	v.Write([]byte("main\r\nshell$ "))
	v.Write([]byte("\x1b[?1049h\x1b[Hfull\x1b[3;1Hscreen"))
	screenIs(t, v, "full", "", "screen")
	v.Write([]byte("\x1b[?1049l"))
	screenIs(t, v, "main", "shell$")
	if v.x != 7 || v.y != 1 {
		t.Errorf("cursor at %d,%d after the alternate screen", v.x, v.y)
	}
}

func TestCharsetsAndUnicode(t *testing.T) {
	v := newVT(12, 2)
	v.Write([]byte("\x1b(0lqk\x1b(B x\x1b)0\x0eq\x0fq"))
	screenIs(t, v, "┌─┐ x─q")
	v.Write([]byte("\r\n\xe6\x97"))
	v.Write([]byte("\xa5b\xcc\x81c\x1b]0;title\x07d\x1bPq#0\x1b\\e"))
	screenIs(t, v, "┌─┐ x─q", "日bcde")
	if v.grid[1][1].r != 0 {
		t.Errorf("a wide character takes one cell")
	}
	v.Write([]byte("\x1b[2;2HZ"))
	screenIs(t, v, "┌─┐ x─q", " Zbcde")
}

func TestEncode(t *testing.T) {
	v := newVT(30, 1)
	v.Write([]byte("ab  \x1b[7mrev\x1b[m          far\x1b[1;30H\x1b[4mu"))
	glyph := func(r rune) uint32 { return uint32(r) }
	rec := encode(v.grid[0], glyph)
	type run struct {
		x, cells int
		st, line byte
		text     string
	}
	var runs []run
	for i, at := 0, 1; i < int(rec[0]); i++ {
		n := int(rec[at+4])
		var s strings.Builder
		for _, g := range unvarints(rec[at+5 : at+5+n]) {
			s.WriteRune(rune(g))
		}
		runs = append(runs, run{int(rec[at]), int(rec[at+1]), rec[at+2], rec[at+3], s.String()})
		at += 5 + n
	}
	want := []run{{0, 2, defaultLevel, 0, "ab"}, {4, 3, defaultLevel << 4, 0, "rev"}, {17, 3, defaultLevel, 0, "far"},
		{29, 1, defaultLevel, 1, "u"}}
	if len(runs) != len(want) {
		t.Fatalf("runs %+v", runs)
	}
	for i := range want {
		if runs[i] != want[i] {
			t.Errorf("run %d: %+v, want %+v", i, runs[i], want[i])
		}
	}
}

// The ring turns by a scroll when that sends fewer rows; the cursor's row
// is sent again when the cursor moves.
func TestScreenFrames(t *testing.T) {
	rows := func(s ...string) []row {
		var out []row
		for _, x := range s {
			out = append(out, row{rec: []byte(x), cursor: -1})
		}
		return out
	}
	s := screen{slots: make([]string, 4), fresh: true}
	f, ok := s.next(rows("a", "b", "c", "d"), 0)
	if !ok || !f.full || len(f.recs) != 4 {
		t.Fatalf("first frame %+v", f)
	}
	if _, ok := s.next(rows("a", "b", "c", "d"), 0); ok {
		t.Fatalf("a frame without a change")
	}
	f, _ = s.next(rows("c", "d", "e", "f"), 2)
	if f.top != 2 || !f.full || len(f.recs) != 2 || string(f.recs[0]) != "e" || string(f.recs[1]) != "f" {
		t.Fatalf("scroll frame %+v", f)
	}
	r := rows("c", "d", "e", "f")
	r[3].cursor = 1
	f, _ = s.next(r, 0)
	if f.full || len(f.recs) != 1 || f.curSlot != 1 || f.curX != 1 {
		t.Fatalf("cursor frame %+v", f)
	}
}
