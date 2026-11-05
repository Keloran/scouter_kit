package scouterkit

/*
#include <stdlib.h>
#include "scouter_kit_c.h"

const unsigned char *sk_go_departure_mono(size_t *size);
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

type Align int

const (
	AlignLeft Align = iota
	AlignCenter
	AlignRight
)

// Font is a font in the glasses' memory. It stays valid until Unload,
// UnloadAll or Deinit; after that its methods do nothing.
type Font struct {
	c           *C.sk_font
	gen         int
	Ascent      int // pixels above the baseline
	Descent     int // pixels below the baseline
	LineSpacing int // baseline-to-baseline distance
}

// Extent is the size of a line of text.
type Extent struct {
	Width   int // advance width
	Height  int // height of the ink
	Ascent  int // ink above the baseline
	Descent int // ink below the baseline
}

// DepartureMono returns the built-in font blob: Departure Mono 11 px (SIL
// Open Font License 1.1).
func DepartureMono() []byte {
	var size C.size_t
	p := C.sk_go_departure_mono(&size)
	return C.GoBytes(unsafe.Pointer(p), C.int(size))
}

// LoadFont loads a font blob made by font_converter.py. glyphs is the text of
// the characters to load (empty: all of them); the space and U+FFFD are
// added. scale > 1 enlarges the glyphs of pixel fonts.
func LoadFont(blob []byte, glyphs string, scale int) (*Font, error) {
	return loadFont(blob, glyphs, scale, C.SK_KEEP_SESSION)
}

// LoadFontStored is LoadFont that also keeps the font in each arm's flash
// (VM.md §6.10): a reboot or a later session loads it from there instead of
// sending it again. An arm without the store gets it as LoadFont does.
func LoadFontStored(blob []byte, glyphs string, scale int) (*Font, error) {
	return loadFont(blob, glyphs, scale, C.SK_KEEP_STORED)
}

func loadFont(blob []byte, glyphs string, scale int, keep C.int) (*Font, error) {
	if len(blob) == 0 {
		return nil, errors.New("scouterkit: empty font blob")
	}
	text := C.CString(glyphs)
	defer C.free(unsafe.Pointer(text))
	var msg [256]C.char
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return nil, ErrNotInitialized
	}
	c := C.sk_load_font_keep(unsafe.Pointer(&blob[0]), C.size_t(len(blob)), text, C.int(scale), keep,
		&msg[0], C.size_t(len(msg)))
	if c == nil {
		return nil, fmt.Errorf("scouterkit: %s", C.GoString(&msg[0]))
	}
	var ascent, descent, spacing C.int
	C.sk_font_metrics(c, &ascent, &descent, &spacing)
	return &Font{c: c, gen: generation, Ascent: int(ascent), Descent: int(descent),
		LineSpacing: int(spacing)}, nil
}

// Measure returns the size of a line of UTF-8 text.
func (f *Font) Measure(text string) Extent {
	s := C.CString(text)
	defer C.free(unsafe.Pointer(s))
	mu.RLock()
	defer mu.RUnlock()
	var w, h, a, d C.int
	if c := f.handle(); c != nil {
		C.sk_font_measure(c, s, &w, &h, &a, &d)
	}
	return Extent{Width: int(w), Height: int(h), Ascent: int(a), Descent: int(d)}
}

// DrawLine draws one line of UTF-8 text into the bitmap; the glasses draw the
// glyphs themselves. x is the left edge, the centre or the right edge of the
// text, depending on align. It returns the advance width of the text.
func (f *Font) DrawLine(b *Bitmap, text string, x, baseline int, align Align, gray uint8) int {
	s := C.CString(text)
	defer C.free(unsafe.Pointer(s))
	mu.RLock()
	defer mu.RUnlock()
	c, bc := f.handle(), b.handle()
	if c == nil || bc == nil {
		return 0
	}
	return int(C.sk_font_draw_line(c, bc, s, C.int(x), C.int(baseline), C.int(align), C.uint8_t(gray)))
}

// Unload frees the font's memory on the glasses.
func (f *Font) Unload() {
	mu.Lock()
	defer mu.Unlock()
	if c := f.handle(); c != nil {
		C.sk_unload_font(c)
		f.c = nil
	}
}

func (f *Font) handle() *C.sk_font {
	if f == nil || f.gen != generation {
		return nil
	}
	return f.c
}
