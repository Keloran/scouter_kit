package main

import (
	"bytes"
	"encoding/binary"
	"math"
	"testing"
)

func TestFractionalFontRasterAndMetrics(t *testing.T) {
	// One 2x2 glyph, signed offsets and one negative kerning adjustment.
	src := make([]byte, 38)
	copy(src, "SKF\x01")
	binary.LittleEndian.PutUint32(src[4:], uint32(len(src)))
	binary.LittleEndian.PutUint16(src[8:], 1)
	binary.LittleEndian.PutUint16(src[10:], 1)
	src[12], src[13], src[14] = 2, 1, 3
	binary.LittleEndian.PutUint32(src[16:], 37*8)
	src[20], src[21], src[22] = 2, 2, 2
	binary.LittleEndian.PutUint16(src[24:], 65535) // -1
	binary.LittleEndian.PutUint16(src[26:], 65534) // -2
	binary.LittleEndian.PutUint32(src[28:], '0')
	src[36], src[37] = 255, 0x90 // kerning -1; pixels 10/01
	out, err := scaleFontBlob(src, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out[37:], []byte{0xd8, 0x80}) { // pixels 110/110/001
		t.Fatalf("resampled pixels: %x", out[37:])
	}
	if !bytes.Equal(out[12:15], []byte{3, 2, 5}) || !bytes.Equal(out[20:23], []byte{3, 3, 3}) {
		t.Fatalf("scaled metrics: %v %v", out[12:15], out[20:23])
	}
	if int16(binary.LittleEndian.Uint16(out[24:])) != -2 || int16(binary.LittleEndian.Uint16(out[26:])) != -3 || int8(out[36]) != -2 {
		t.Fatal("signed offsets or kerning scaled incorrectly")
	}
	if binary.LittleEndian.Uint32(out[4:]) != uint32(len(out)) || binary.LittleEndian.Uint32(out[28:]) != '0' {
		t.Fatal("font size or codepoint table corrupted")
	}
	if src[37] != 0x90 {
		t.Fatal("source font changed")
	}
	for _, scale := range []float64{0, 4, math.NaN(), math.Inf(1)} {
		if _, err := scaleFontBlob(src, scale); err == nil {
			t.Fatalf("invalid scale accepted: %g", scale)
		}
	}
	for i := 0; i < len(src); i++ {
		if _, err := scaleFontBlob(src[:i], 1.5); err == nil {
			t.Fatalf("truncated font accepted at length %d", i)
		}
	}
}
