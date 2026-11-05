package demo

import (
	"image"

	"scouterkit"
)

// Image holds panel levels 0...15, one byte per pixel.
type Image struct {
	W, H int
	Pix  []uint8
}

func NewImage(w, h int) *Image { return &Image{W: w, H: h, Pix: make([]uint8, w*h)} }

// NewPanel is a black 640 x 480 panel.
func NewPanel() *Image { return NewImage(scouterkit.PanelWidth, scouterkit.PanelHeight) }

func (m *Image) At(x, y int) uint8 {
	if x < 0 || y < 0 || x >= m.W || y >= m.H {
		return 0
	}
	return m.Pix[y*m.W+x]
}

func (m *Image) Set(x, y int, v uint8) {
	if x >= 0 && y >= 0 && x < m.W && y < m.H {
		m.Pix[y*m.W+x] = v
	}
}

func (m *Image) Fill(x, y, w, h int, v uint8) {
	for r := y; r < y+h; r++ {
		for c := x; c < x+w; c++ {
			m.Set(c, r, v)
		}
	}
}

// Paste copies src with its top left corner at (x, y), clipped.
func (m *Image) Paste(src *Image, x, y int) {
	for r := 0; r < src.H; r++ {
		for c := 0; c < src.W; c++ {
			m.Set(x+c, y+r, src.Pix[r*src.W+c])
		}
	}
}

// Rows packs rows y0...y0+rows-1 at 4 bpp, the left pixel in the high
// nibble: the format of the panel read-back and of 4-bpp bitmaps.
func (m *Image) Rows(y0, rows int) []byte {
	stride := (m.W + 1) / 2
	out := make([]byte, rows*stride)
	for r := 0; r < rows; r++ {
		for x := 0; x < m.W; x++ {
			v := m.At(x, y0+r)
			if x%2 == 0 {
				v <<= 4
			}
			out[r*stride+x/2] |= v
		}
	}
	return out
}

// Gray is the image as 8-bit gray (level x 17).
func (m *Image) Gray() *image.Gray {
	g := image.NewGray(image.Rect(0, 0, m.W, m.H))
	for i, v := range m.Pix {
		g.Pix[i] = v * 17
	}
	return g
}

// Equal reports whether two images are the same.
func (m *Image) Equal(o *Image) bool {
	if m.W != o.W || m.H != o.H {
		return false
	}
	for i := range m.Pix {
		if m.Pix[i] != o.Pix[i] {
			return false
		}
	}
	return true
}

// FromPanel takes the levels of an image made by Arm.EmulatedPanel.
func FromPanel(g *image.Gray) *Image {
	m := NewImage(g.Rect.Dx(), g.Rect.Dy())
	for i := range m.Pix {
		m.Pix[i] = g.Pix[i] / 17
	}
	return m
}

// Diff counts the pixels that differ and returns the first one.
func Diff(a, b *Image) (n, x, y int) {
	x, y = -1, -1
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			if n == 0 {
				x, y = i%a.W, i/a.W
			}
			n++
		}
	}
	return n, x, y
}

// PackBits encodes data for WRITE_RLE and UNPACK (VM.md §2.4).
func PackBits(data []byte) []byte {
	var out []byte
	run := func(i int) int {
		n := 1
		for i+n < len(data) && n < 128 && data[i+n] == data[i] {
			n++
		}
		return n
	}
	for i := 0; i < len(data); {
		if n := run(i); n >= 3 {
			out = append(out, byte(257-n), data[i])
			i += n
			continue
		}
		j := i
		for j < len(data) && j-i < 128 && !(j+2 < len(data) && data[j] == data[j+1] && data[j] == data[j+2]) {
			j++
		}
		out = append(out, byte(j-i-1))
		out = append(out, data[i:j]...)
		i = j
	}
	return out
}

// Unpack decodes PackBits as the arms do; false unless the runs use
// exactly the input and produce exactly n bytes.
func Unpack(packed []byte, n int) ([]byte, bool) {
	out := make([]byte, 0, n)
	for i := 0; i < len(packed); {
		h := int8(packed[i])
		i++
		switch {
		case h >= 0:
			if i+int(h)+1 > len(packed) {
				return nil, false
			}
			out = append(out, packed[i:i+int(h)+1]...)
			i += int(h) + 1
		case h != -128:
			if i >= len(packed) {
				return nil, false
			}
			for k := 0; k < 1-int(h); k++ {
				out = append(out, packed[i])
			}
			i++
		}
		if len(out) > n {
			return nil, false
		}
	}
	return out, len(out) == n
}
