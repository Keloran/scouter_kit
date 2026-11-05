package scouterkit

/*
#include <stdlib.h>
#include "scouter_kit_c.h"
*/
import "C"

import (
	"runtime"
	"unsafe"
)

// VideoState is the size of a VIDEO decoder's state in the arena.
const VideoState = 1596

// VideoEncoder makes VIDEO records (VM.md §3.7) for an arm whose 1-bpp
// bitmap starts zeroed: give every record to the arm, in order; the first
// one resets the decoder's state.
type VideoEncoder struct {
	c    *C.sk_g1_video_encoder
	w, h int
}

// NewVideoEncoder makes an encoder for frames of w x h pixels; it panics
// outside 1...640 x 1...480.
func NewVideoEncoder(w, h int) *VideoEncoder {
	if w < 1 || w > PanelWidth || h < 1 || h > PanelHeight {
		panic("scouterkit: NewVideoEncoder: size out of range")
	}
	e := &VideoEncoder{c: C.sk_g1_video_encoder_new(C.int(w), C.int(h)), w: w, h: h}
	runtime.SetFinalizer(e, (*VideoEncoder).Close)
	return e
}

// Stride is the least bytes per row of the arm's bitmap.
func (e *VideoEncoder) Stride() int { return (e.w + 7) / 8 }

// Encode returns the record of the next frame: w x h bytes, nonzero = ink.
// redraw: the arm draws every row, not only the changed ones. A record over
// max bytes (max > 0) is not made: nil, and the encoder is unchanged.
func (e *VideoEncoder) Encode(pix []byte, redraw bool, max int) []byte {
	if len(pix) < e.w*e.h {
		panic("scouterkit: VideoEncoder.Encode: frame too small")
	}
	r := 0
	if redraw {
		r = 1
	}
	limit := C.size_t(^C.size_t(0))
	if max > 0 {
		limit = C.size_t(max)
	}
	var n C.size_t
	p := C.sk_g1_video_encode(e.c, (*C.uint8_t)(unsafe.Pointer(&pix[0])), C.int(r), limit, &n)
	runtime.KeepAlive(e)
	if p == nil {
		return nil
	}
	return C.GoBytes(unsafe.Pointer(p), C.int(n))
}

// Close frees the encoder.
func (e *VideoEncoder) Close() {
	if e.c != nil {
		C.sk_g1_video_encoder_free(e.c)
		e.c = nil
	}
}

// Video queues VIDEO: decodes the record of n bytes at src into the 1-bpp
// bitmap and draws the rows that changed at (x, y), ink as level.
func (a Arm) Video(bitmap, stride, w, h uint32, x, y int32, level uint8, src, n, state uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_video(C.int(a), C.uint32_t(bitmap), C.uint32_t(stride), C.uint32_t(w), C.uint32_t(h), C.int32_t(x),
		C.int32_t(y), C.uint8_t(level), C.uint32_t(src), C.uint32_t(n), C.uint32_t(state))
}
