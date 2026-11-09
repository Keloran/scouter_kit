package main

import (
	"encoding/binary"
	"errors"
	"math"

	"scouterkit"
)

func loadDisplayFont(glyphs string, scale float64) (*scouterkit.Font, error) {
	if err := (displaySettings{FontScale: scale}).validate(); err != nil {
		return nil, err
	}
	blob := scouterkit.DepartureMono()
	if scale == math.Trunc(scale) {
		return scouterkit.LoadFont(blob, glyphs, int(scale))
	}
	blob, err := scaleFontBlob(blob, scale)
	if err != nil {
		return nil, err
	}
	return scouterkit.LoadFont(blob, glyphs, 1)
}

// Resample the built-in 1-bpp font before loading it. The library's scale
// parameter is integer-only; keeping this conversion here leaves its API unchanged.
func scaleFontBlob(src []byte, scale float64) ([]byte, error) {
	if err := (displaySettings{FontScale: scale}).validate(); err != nil {
		return nil, err
	}
	invalid := errors.New("invalid font blob")
	if len(src) < 16 || string(src[:4]) != "SKF\x01" {
		return nil, invalid
	}
	u32, u16 := binary.LittleEndian.Uint32, binary.LittleEndian.Uint16
	n, k := int(u16(src[8:])), int(u16(src[10:]))
	head := 16 + 16*n + 5*k
	if n == 0 || head > len(src) || int(u32(src[4:])) != len(src) {
		return nil, invalid
	}
	out := append([]byte(nil), src[:head]...)
	round := func(v int) int { return int(math.Round(float64(v) * scale)) }
	for i := 12; i <= 14; i++ {
		out[i] = byte(min(255, round(int(src[i]))))
	}
	out[14] = max(1, out[14]) // The dashboard divides by line spacing, including at tiny scales.
	for i := 0; i < n; i++ {
		off := 16 + 12*i
		e := src[off : off+12]
		bits := uint64(u32(e))
		w, h := int(e[4]), int(e[5])
		if bits+uint64(w*h) > uint64(len(src))*8 {
			return nil, invalid
		}
		nw, nh, advance := round(w), round(h), round(int(e[6]))
		if nw > 255 || nh > 255 || advance > 255 {
			return nil, errors.New("scaled font glyph exceeds 255 pixels")
		}
		dst := out[off : off+12]
		binary.LittleEndian.PutUint32(dst, uint32(len(out)*8))
		dst[4], dst[5], dst[6] = byte(nw), byte(nh), byte(advance)
		for _, j := range []int{8, 10} {
			v := round(int(int16(u16(e[j:]))))
			if v < -32768 || v > 32767 {
				return nil, errors.New("scaled font offset exceeds signed 16-bit range")
			}
			binary.LittleEndian.PutUint16(dst[j:], uint16(int16(v)))
		}
		pixels := make([]byte, (nw*nh+7)/8)
		for y := 0; y < nh && w > 0; y++ {
			for x := 0; x < nw; x++ {
				sx, sy := min(w-1, int(float64(x)/scale)), min(h-1, int(float64(y)/scale))
				bit := bits + uint64(sy*w+sx)
				if src[bit/8]&(1<<(7-bit%8)) != 0 {
					p := y*nw + x
					pixels[p/8] |= 1 << (7 - p%8)
				}
			}
		}
		out = append(out, pixels...)
	}
	for i := 0; i < k; i++ {
		off := 16 + 16*n + 4*k + i
		out[off] = byte(int8(max(-128, min(127, round(int(int8(src[off])))))))
	}
	binary.LittleEndian.PutUint32(out[4:], uint32(len(out)))
	return out, nil
}
