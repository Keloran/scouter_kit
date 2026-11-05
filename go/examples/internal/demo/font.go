package demo

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"

	"scouterkit"
)

// Font is a font blob (VM.md §4.2), read to draw glyph runs the way GLYPHS
// does: the host's expectation of what a program draws.
type Font struct {
	b                            []byte
	n, k                         int
	Ascent, Descent, LineSpacing int
}

func ParseFont(b []byte) (*Font, error) {
	if len(b) < 16 || string(b[:3]) != "SKF" || b[3] != 1 {
		return nil, errors.New("not a font blob of format 1")
	}
	size := int(binary.LittleEndian.Uint32(b[4:]))
	n, k := int(binary.LittleEndian.Uint16(b[8:])), int(binary.LittleEndian.Uint16(b[10:]))
	if size > len(b) || n == 0 || 16+16*n+5*k > size {
		return nil, fmt.Errorf("font blob: size %d, %d glyphs, %d kerning pairs in %d bytes", size, n, k, len(b))
	}
	return &Font{b: b[:size], n: n, k: k, Ascent: int(b[12]), Descent: int(b[13]), LineSpacing: int(b[14])}, nil
}

// FontOf reads a loaded font from the library's mirror of the arm.
func FontOf(arm scouterkit.Arm, f *scouterkit.Font) (*Font, error) {
	head := arm.Mirror(f.Address(), 16)
	if len(head) < 16 {
		return nil, errors.New("font not in the mirror")
	}
	return ParseFont(arm.Mirror(f.Address(), int(binary.LittleEndian.Uint32(head[4:]))))
}

func (f *Font) glyph(i int) (bits uint32, w, h, advance, xOff, yOff int) {
	e := f.b[16+12*i:]
	return binary.LittleEndian.Uint32(e), int(e[4]), int(e[5]), int(e[6]),
		int(int16(binary.LittleEndian.Uint16(e[8:]))), int(int16(binary.LittleEndian.Uint16(e[10:])))
}

func (f *Font) kern(p, g int) int {
	keys := 16 + 16*f.n
	key := uint32(p)<<16 | uint32(g)
	i := sort.Search(f.k, func(i int) bool { return binary.LittleEndian.Uint32(f.b[keys+4*i:]) >= key })
	if i < f.k && binary.LittleEndian.Uint32(f.b[keys+4*i:]) == key {
		return int(int8(f.b[keys+4*f.k+i]))
	}
	return 0
}

// Draw draws the glyphs with the pen at (x, baseline y), MASK mode at level,
// and returns the advance.
func (f *Font) Draw(dst *Image, x, y int, glyphs []uint32, level uint8) int {
	pen, prev := x, -1
	for _, gi := range glyphs {
		g := int(gi)
		if g >= f.n {
			break
		}
		if prev >= 0 {
			pen += f.kern(prev, g)
		}
		bits, w, h, advance, xOff, yOff := f.glyph(g)
		for r := 0; r < h; r++ {
			for c := 0; c < w; c++ {
				p := bits + uint32(r*w+c)
				if f.b[p/8]>>(7-p%8)&1 == 1 {
					dst.Set(pen+xOff+c, y+yOff+r, level)
				}
			}
		}
		pen += advance
		prev = g
	}
	return pen - x
}

// Varints encodes glyph indices as GLYPHS reads them (LEB128).
func Varints(glyphs []uint32) []byte {
	var out []byte
	for _, g := range glyphs {
		for g >= 0x80 {
			out = append(out, byte(g)|0x80)
			g >>= 7
		}
		out = append(out, byte(g))
	}
	return out
}
