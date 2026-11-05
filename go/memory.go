package scouterkit

/*
#include <stdlib.h>
#include "scouter_kit_c.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Alloc reserves n bytes of arena memory, at the same address on both arms
// (a multiple of 4), shared with the fonts and bitmaps.
func Alloc(n int) (uint32, error) {
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return 0, ErrNotInitialized
	}
	at := int64(C.sk_g1_alloc(C.uint32_t(n)))
	if at < 0 {
		return 0, fmt.Errorf("scouterkit: %d bytes: out of device memory", n)
	}
	return uint32(at), nil
}

// Free releases memory from Alloc.
func Free(address uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_free(C.uint32_t(address))
}

// Address is where the bitmap lives in both arms' arena (4 bpp, stride bytes
// per row), for programs.
func (b *Bitmap) Address() (address uint32, stride int) {
	mu.RLock()
	defer mu.RUnlock()
	c := b.handle()
	if c == nil {
		return 0, 0
	}
	var s C.int
	a := C.sk_g1_bitmap_address(c, &s)
	return uint32(a), int(s)
}

// Mirror returns n bytes at address of the library's mirror of the arm's
// arena: what the host wrote there (a font blob, a table), not what programs
// changed. Nil if the range is not in the arena or before Init.
func (a Arm) Mirror(address uint32, n int) []byte {
	mu.RLock()
	defer mu.RUnlock()
	if !initialized || n < 0 || uint64(address)+uint64(n) > arenaBytes {
		return nil
	}
	arena := unsafe.Slice((*byte)(unsafe.Pointer(C.sk_g1_arena(C.int(a)))), arenaBytes)
	return append([]byte(nil), arena[address:address+uint32(n)]...)
}

const arenaBytes = 262144

// Address is where the font blob lives in both arms' arena, for programs.
func (f *Font) Address() uint32 {
	mu.RLock()
	defer mu.RUnlock()
	c := f.handle()
	if c == nil {
		return 0
	}
	return uint32(C.sk_g1_font_address(c))
}

// GlyphIndices are the font's glyphs for the text, as GLYPHS draws them: a
// digit's index is its value (VM.md §4.1); a missing character is U+FFFD's
// glyph, or left out.
func (f *Font) GlyphIndices(text string) []uint32 {
	mu.RLock()
	defer mu.RUnlock()
	c := f.handle()
	if c == nil {
		return nil
	}
	ct := C.CString(text)
	defer C.free(unsafe.Pointer(ct))
	n := C.sk_g1_glyph_indices(c, ct, nil, 0)
	if n == 0 {
		return nil
	}
	out := make([]uint32, int(n))
	C.sk_g1_glyph_indices(c, ct, (*C.uint32_t)(unsafe.Pointer(&out[0])), n)
	return out
}
