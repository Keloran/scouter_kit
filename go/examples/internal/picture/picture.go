// Package picture prepares pictures for the examples.
package picture

import (
	"image"
	"image/draw"
	"image/png"
	"os"
)

// Luma converts img to gray, composited over black.
func Luma(img image.Image) *image.Gray {
	g := image.NewGray(img.Bounds())
	draw.Draw(g, g.Rect, img, img.Bounds().Min, draw.Src)
	return g
}

// Trim cuts off the dark margin: black is see-through on the glasses.
func Trim(g *image.Gray, threshold uint8) *image.Gray {
	box := image.Rectangle{}
	for y := g.Rect.Min.Y; y < g.Rect.Max.Y; y++ {
		for x := g.Rect.Min.X; x < g.Rect.Max.X; x++ {
			if g.GrayAt(x, y).Y > threshold {
				box = box.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	if box.Empty() {
		return g
	}
	return g.SubImage(box).(*image.Gray)
}

// Fit shrinks g to fit in w x h, averaging the source pixels under each new
// pixel. Pictures that fit are kept 1:1.
func Fit(g *image.Gray, w, h int) *image.Gray {
	sw, sh := g.Rect.Dx(), g.Rect.Dy()
	if sw <= w && sh <= h {
		return g
	}
	dw, dh := w, max(1, sh*w/sw)
	if dh > h {
		dw, dh = max(1, sw*h/sh), h
	}
	out := image.NewGray(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		y0, y1 := g.Rect.Min.Y+y*sh/dh, g.Rect.Min.Y+(y+1)*sh/dh
		for x := 0; x < dw; x++ {
			x0, x1 := g.Rect.Min.X+x*sw/dw, g.Rect.Min.X+(x+1)*sw/dw
			sum := 0
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					sum += int(g.GrayAt(sx, sy).Y)
				}
			}
			n := (y1 - y0) * (x1 - x0)
			out.Pix[y*out.Stride+x] = uint8((sum + n/2) / n)
		}
	}
	return out
}

func SavePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
