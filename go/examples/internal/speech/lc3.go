package speech

/*
#cgo CFLAGS: -DLC3_PLUS=0 -DLC3_PLUS_HR=0 -I${SRCDIR}/../../../../third_party/liblc3/include
#include <stdlib.h>
#include <lc3.h>
*/
import "C"

import (
	"fmt"
	"math"
	"unsafe"

	_ "scouterkit/internal/lc3"
)

// Codec passes samples through the microphone's link as the arm encodes
// them and the library decodes them: LC3, 10 ms frames at 32 or 16 kbps.
func Codec(pcm []float32, kbps int) ([]float32, error) {
	if kbps != 32 && kbps != 16 {
		return nil, fmt.Errorf("speech: LC3 at %d kbps: 32 or 16", kbps)
	}
	const dt, rate, n = 10000, 16000, 160
	bytes := kbps * 1000 / 8 / 100
	encMem := C.malloc(C.size_t(C.lc3_encoder_size(dt, rate)))
	decMem := C.malloc(C.size_t(C.lc3_decoder_size(dt, rate)))
	buf := (*C.int16_t)(C.malloc(2 * n))
	frame := C.malloc(C.size_t(bytes))
	defer C.free(encMem)
	defer C.free(decMem)
	defer C.free(unsafe.Pointer(buf))
	defer C.free(frame)
	enc := C.lc3_setup_encoder(dt, rate, 0, encMem)
	dec := C.lc3_setup_decoder(dt, rate, 0, decMem)
	samples := unsafe.Slice((*int16)(unsafe.Pointer(buf)), n)
	out := make([]float32, 0, len(pcm))
	for k := 0; (k+1)*n <= len(pcm); k++ {
		for i, v := range pcm[k*n : (k+1)*n] {
			samples[i] = int16(max(-32768, min(32767, math.Round(float64(v)*32768))))
		}
		if C.lc3_encode(enc, C.LC3_PCM_FORMAT_S16, unsafe.Pointer(buf), 1, C.int(bytes), frame) != 0 {
			return nil, fmt.Errorf("speech: lc3_encode failed")
		}
		if C.lc3_decode(dec, frame, C.int(bytes), C.LC3_PCM_FORMAT_S16, unsafe.Pointer(buf), 1) < 0 {
			return nil, fmt.Errorf("speech: lc3_decode failed")
		}
		for _, v := range samples {
			out = append(out, float32(v)/32768)
		}
	}
	return out, nil
}
