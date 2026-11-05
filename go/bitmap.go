package scouterkit

/*
#include "scouter_kit_c.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"image"
	"image/draw"
	"math"
	"unsafe"
)

// Bitmap is a gray image in the glasses' memory. It stays valid until
// Unload, UnloadAll or Deinit; after that its methods do nothing.
type Bitmap struct {
	c      *C.sk_bitmap
	gen    int
	width  int
	height int
}

// CreateBitmap reserves a bitmap with all pixels 0.
func CreateBitmap(width, height int) (*Bitmap, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("scouterkit: bad bitmap size %dx%d", width, height)
	}
	mu.RLock()
	defer mu.RUnlock()
	c := C.sk_create_bitmap(C.int(width), C.int(height))
	if c == nil {
		return nil, bitmapError(width, height)
	}
	return &Bitmap{c: c, gen: generation, width: width, height: height}, nil
}

// LoadBitmap creates a bitmap the size of img and writes img into it (see
// Write).
func LoadBitmap(img image.Image) (*Bitmap, error) {
	return loadBitmap(img, C.SK_KEEP_SESSION)
}

// LoadBitmapStored is LoadBitmap that also keeps the bitmap's pixels (as they
// are at its first Flush) in each arm's flash (VM.md §6.10), so a reboot or a
// later session loads them from there.
func LoadBitmapStored(img image.Image) (*Bitmap, error) {
	return loadBitmap(img, C.SK_KEEP_STORED)
}

func loadBitmap(img image.Image, keep C.int) (*Bitmap, error) {
	g := toGray(img)
	r := g.Bounds()
	if r.Empty() {
		return nil, errors.New("scouterkit: empty image")
	}
	mu.RLock()
	defer mu.RUnlock()
	c := C.sk_load_bitmap_keep(pixels(g), C.int(r.Dx()), C.int(r.Dy()), C.int(g.Stride),
		C.SK_DITHER_FLOYD_STEINBERG, keep)
	if c == nil {
		return nil, bitmapError(r.Dx(), r.Dy())
	}
	return &Bitmap{c: c, gen: generation, width: r.Dx(), height: r.Dy()}, nil
}

func bitmapError(width, height int) error {
	if !initialized {
		return ErrNotInitialized
	}
	return fmt.Errorf("scouterkit: no room for a %dx%d bitmap (%d of %d bytes free)",
		width, height, int(C.sk_free_memory()), int(C.sk_total_memory()))
}

// Bounds returns the bitmap's rectangle, with (0, 0) at the top left.
func (b *Bitmap) Bounds() image.Rectangle {
	return image.Rect(0, 0, b.width, b.height)
}

// Write puts img into the bitmap with its top left corner at (x, y): its
// luma goes through the response curve (SetResponse) and Floyd–Steinberg
// dithering to the 16 levels. With the identity curve, an image whose pixels
// are all multiples of 17 is kept exactly. Transparent parts count as black.
func (b *Bitmap) Write(img image.Image, x, y int) {
	g := toGray(img)
	r := g.Bounds()
	if r.Empty() {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	if c := b.handle(); c != nil {
		C.sk_write(c, pixels(g), C.int(r.Dx()), C.int(r.Dy()), C.int(g.Stride), C.int(x), C.int(y),
			C.SK_DITHER_FLOYD_STEINBERG)
	}
}

// Fill sets the pixels of r (clipped to the bitmap) to gray.
func (b *Bitmap) Fill(r image.Rectangle, gray uint8) {
	r = r.Canon()
	if r.Empty() {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	if c := b.handle(); c != nil {
		C.sk_fill(c, C.int(r.Min.X), C.int(r.Min.Y), C.int(r.Dx()), C.int(r.Dy()), C.uint8_t(gray))
	}
}

// Image returns a copy of the bitmap's pixels (nil once unloaded).
func (b *Bitmap) Image() *image.Gray {
	mu.Lock()
	defer mu.Unlock()
	c := b.handle()
	if c == nil {
		return nil
	}
	return copyView(C.sk_bitmap_contents(c))
}

// Unload frees the bitmap's memory on the glasses.
func (b *Bitmap) Unload() {
	mu.Lock()
	defer mu.Unlock()
	if c := b.handle(); c != nil {
		C.sk_unload_bitmap(c)
		b.c = nil
	}
}

func (b *Bitmap) handle() *C.sk_bitmap {
	if b == nil || b.gen != generation {
		return nil
	}
	return b.c
}

// toGray converts img to its luma, composited over black.
func toGray(img image.Image) *image.Gray {
	if g, ok := img.(*image.Gray); ok {
		return g
	}
	r := img.Bounds()
	g := image.NewGray(r)
	draw.Draw(g, r, img, r.Min, draw.Src)
	return g
}

func pixels(g *image.Gray) *C.uint8_t {
	r := g.Bounds()
	return (*C.uint8_t)(unsafe.Pointer(&g.Pix[g.PixOffset(r.Min.X, r.Min.Y)]))
}

// SetResponse sets the response curve that Write and LoadBitmap apply to
// every pixel before dithering. It starts as the identity.
func SetResponse(curve [256]uint8) {
	mu.Lock()
	defer mu.Unlock()
	copy(unsafe.Slice((*uint8)(unsafe.Pointer(C.sk_response())), 256), curve[:])
}

// Response returns the current response curve.
func Response() [256]uint8 {
	mu.Lock()
	defer mu.Unlock()
	var curve [256]uint8
	copy(curve[:], unsafe.Slice((*uint8)(unsafe.Pointer(C.sk_response())), 256))
	return curve
}

// GammaResponse returns the curve 255 * (v / 255)^gamma. The G1's levels are
// roughly linear in luminance, so a gamma near 2.2 moves picture midtones to
// the low levels, where the steps are visible.
func GammaResponse(gamma float64) [256]uint8 {
	var curve [256]uint8
	for v := range curve {
		curve[v] = uint8(math.Round(255 * math.Pow(float64(v)/255, gamma)))
	}
	return curve
}
