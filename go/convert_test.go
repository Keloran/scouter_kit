package scouterkit

import (
	"bytes"
	"crypto/sha256"
	"image"
	"image/color"
	"testing"
)

func TestToGrayUsesLuma(t *testing.T) {
	src := image.NewRGBA(image.Rect(10, 20, 14, 21))
	src.Set(10, 20, color.RGBA{255, 0, 0, 255})
	src.Set(11, 20, color.RGBA{0, 255, 0, 255})
	src.Set(12, 20, color.RGBA{0, 0, 255, 255})
	src.Set(13, 20, color.RGBA{255, 255, 255, 255})
	g := toGray(src)
	if g.Bounds() != src.Bounds() {
		t.Fatalf("bounds %v, want %v", g.Bounds(), src.Bounds())
	}
	for i, want := range []uint8{76, 150, 29, 255} {
		if got := g.GrayAt(10+i, 20).Y; got != want {
			t.Errorf("pixel %d: %d, want %d", i, got, want)
		}
	}
}

func TestToGrayTransparentIsBlack(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.NRGBA{255, 255, 255, 128})
	src.Set(1, 0, color.NRGBA{255, 255, 255, 0})
	g := toGray(src)
	if got := g.GrayAt(0, 0).Y; got != 128 {
		t.Errorf("half transparent white: %d, want 128", got)
	}
	if got := g.GrayAt(1, 0).Y; got != 0 {
		t.Errorf("transparent white: %d, want 0", got)
	}

	pal := image.NewPaletted(image.Rect(0, 0, 2, 1), color.Palette{color.RGBA{}, color.White})
	pal.SetColorIndex(1, 0, 1)
	g = toGray(pal)
	if g.GrayAt(0, 0).Y != 0 || g.GrayAt(1, 0).Y != 255 {
		t.Errorf("paletted: %v", g.Pix)
	}
}

func TestToGrayKeepsGray(t *testing.T) {
	src := image.NewGray(image.Rect(0, 0, 8, 8)).SubImage(image.Rect(2, 2, 6, 6)).(*image.Gray)
	if toGray(src) != src {
		t.Error("a gray image was copied")
	}
}

func TestGammaResponse(t *testing.T) {
	identity := GammaResponse(1)
	for v, got := range identity {
		if int(got) != v {
			t.Fatalf("gamma 1: %d -> %d", v, got)
		}
	}
	curve := GammaResponse(2.2)
	if curve[0] != 0 || curve[128] != 56 || curve[255] != 255 {
		t.Errorf("gamma 2.2: 0 -> %d, 128 -> %d, 255 -> %d", curve[0], curve[128], curve[255])
	}
}

// An asset's ID is the first 16 bytes of its SHA-256 (the library's vendored TinyCrypt).
func TestAssetIDOf(t *testing.T) {
	for _, n := range []int{0, 1, 55, 56, 64, 65000} {
		blob := make([]byte, n)
		for i := range blob {
			blob[i] = byte(i * 7)
		}
		sum := sha256.Sum256(blob)
		if id := AssetIDOf(blob); !bytes.Equal(id[:], sum[:16]) {
			t.Errorf("%d bytes: %s, want %x", n, id, sum[:16])
		}
	}
}
