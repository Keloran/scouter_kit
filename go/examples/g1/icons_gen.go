//go:build ignore

// Command icons_gen converts the Pebble icons in ../../../third_party/pebble_icons
// (Apache-2.0, see its NOTICE) into icons/NAME.4bpp: rendered at 50 x 50 by
// rsvg-convert (Debian: librsvg2-bin), the ink as levels 0...15 (the white fill
// and the background 0), two pixels a byte with the left one in the high nibble.
//
//	cd scouter_kit/go && go generate ./examples/g1
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const size = 50

func main() {
	src := filepath.Join("..", "..", "..", "third_party", "pebble_icons")
	files, err := filepath.Glob(filepath.Join(src, "*.svg"))
	if err != nil || len(files) == 0 {
		fail(fmt.Errorf("no icons in %s", src))
	}
	if err := os.MkdirAll("icons", 0o755); err != nil {
		fail(err)
	}
	for _, f := range files {
		out, err := exec.Command("rsvg-convert", "-w", fmt.Sprint(size), "-h", fmt.Sprint(size), "-f", "png", f).Output()
		if err != nil {
			fail(fmt.Errorf("%s: rsvg-convert: %w", f, err))
		}
		img, err := png.Decode(bytes.NewReader(out))
		if err != nil {
			fail(fmt.Errorf("%s: %w", f, err))
		}
		if img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			fail(fmt.Errorf("%s: rendered %v", f, img.Bounds()))
		}
		name := strings.TrimSuffix(filepath.Base(f), ".svg")
		name = strings.TrimSuffix(strings.TrimPrefix(name, "Pebble_50x50_"), "_50px")
		if err := os.WriteFile(filepath.Join("icons", name+".4bpp"), pack(img), 0o644); err != nil {
			fail(err)
		}
	}
	fmt.Printf("%d icons in icons/\n", len(files))
}

// pack composes the icon over white and keeps the ink: coverage minus luma.
func pack(img image.Image) []byte {
	b := img.Bounds()
	out := make([]byte, size/2*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, bl, a := img.At(b.Min.X+x, b.Min.Y+y).RGBA()
			ink := a - min(a, (299*r+587*g+114*bl)/1000)
			level := byte((ink*15 + 32767) / 65535)
			if x%2 == 0 {
				level <<= 4
			}
			out[y*size/2+x/2] |= level
		}
	}
	return out
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "icons_gen:", err)
	os.Exit(1)
}
