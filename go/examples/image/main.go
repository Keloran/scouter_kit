// Command image shows a picture (PNG, JPEG or the first frame of a GIF) on
// both eyes, then reads both panels back to check them.
//
//	go run ./examples/image [flags] FILE
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/picture"
)

func main() {
	gamma := flag.Float64("gamma", 1, "tone curve exponent before dithering (1: the library's curve)")
	save := flag.String("save", "", "write the display read-back to this PNG file")
	left := flag.String("left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	right := flag.String("right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	adapter := flag.String("adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	logs := flag.Bool("log", false, "the library logs to stderr")
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage: image [flags] FILE")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(2)
	}
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter, cfg.Log = *left, *right, *adapter, *logs
	if err := run(flag.Arg(0), *gamma, *save, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "image:", err)
		os.Exit(1)
	}
}

func run(path string, gamma float64, save string, cfg scouterkit.Config) error {
	img, err := decode(path)
	if err != nil {
		return err
	}
	if err := scouterkit.Init(cfg); err != nil {
		return err
	}
	defer scouterkit.Deinit()

	w, h := scouterkit.Size()
	pic := picture.Fit(picture.Trim(picture.Luma(img), 8), w, h)
	if gamma != 1 {
		scouterkit.SetResponse(scouterkit.GammaResponse(gamma))
	}
	b, err := scouterkit.LoadBitmap(pic)
	if err != nil {
		return err
	}
	x, y := (w-pic.Rect.Dx())/2, (h-pic.Rect.Dy())/2
	arms := []scouterkit.Arm{scouterkit.Left, scouterkit.Right}
	before := map[scouterkit.Arm]uint64{}
	for _, arm := range arms {
		before[arm] = arm.State().ShownUpdate
	}
	start := time.Now()
	scouterkit.Draw(b, x, y)
	scouterkit.Flush()
	// Wait until the eyes show it, to time it (PanelCRC would wait too).
	for _, arm := range arms {
		deadline := start.Add(2 * time.Minute)
		for arm.State().Present && arm.State().ShownUpdate == before[arm] && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
	}
	fmt.Printf("%dx%d picture at (%d, %d) shown after %v\n",
		pic.Rect.Dx(), pic.Rect.Dy(), x, y, time.Since(start).Round(time.Millisecond))
	failed := false
	for _, arm := range arms {
		if !arm.State().Present {
			continue
		}
		got, want, err := arm.PanelCRC(0, scouterkit.PanelHeight)
		switch {
		case err != nil:
			fmt.Printf("%s panel: %v\n", arm, err)
			failed = true
		case got != want:
			fmt.Printf("%s panel: read back %08x, expected %08x\n", arm, got, want)
			failed = true
		default:
			fmt.Printf("%s panel: read back %08x, as expected\n", arm, got)
		}
	}
	if save != "" {
		if err := picture.SavePNG(save, scouterkit.Display()); err != nil {
			return err
		}
	}
	if failed {
		return errors.New("the panels do not show the picture")
	}
	return nil
}

func decode(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
