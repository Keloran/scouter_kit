// Command text shows a few lines in the built-in Departure Mono font.
//
//	go run ./examples/text [-save display.png] [-left ADDR -right ADDR] [-adapter hci1]
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/picture"
)

func main() {
	save := flag.String("save", "", "write the display read-back to this PNG file")
	left := flag.String("left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	right := flag.String("right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	adapter := flag.String("adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	logs := flag.Bool("log", false, "the library logs to stderr")
	flag.Parse()
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter, cfg.Log = *left, *right, *adapter, *logs
	if err := run(*save, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "text:", err)
		os.Exit(1)
	}
}

func run(save string, cfg scouterkit.Config) error {
	if err := scouterkit.Init(cfg); err != nil {
		return err
	}
	defer scouterkit.Deinit()

	title, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "ScouterKit", 3)
	if err != nil {
		return err
	}
	body, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 2)
	if err != nil {
		return err
	}
	lines := []struct {
		text string
		gray uint8
	}{
		{"Hello from Go, on both eyes.", 255},
		{"The arms draw the glyphs:", 255},
		{"one small command per line.", 255},
		{"Dimmer text, gray 119.", 119},
	}

	// Both eyes see the columns DX...639.
	width := scouterkit.PanelWidth - cfg.DX
	height := title.LineSpacing + (len(lines)+1)*body.LineSpacing
	page, err := scouterkit.CreateBitmap(width, height)
	if err != nil {
		return err
	}
	y := title.Ascent
	w := title.DrawLine(page, "ScouterKit", width/2, y, scouterkit.AlignCenter, 255)
	page.Fill(image.Rect(width/2-w/2, y+4, width/2+w/2, y+6), 255)
	y = title.LineSpacing + body.Ascent
	for _, line := range lines {
		body.DrawLine(page, line.text, 0, y, scouterkit.AlignLeft, line.gray)
		y += body.LineSpacing
	}
	clock := time.Now().Format("15:04")
	body.DrawLine(page, clock, width, y, scouterkit.AlignRight, 255)

	_, h := scouterkit.Size()
	scouterkit.Draw(page, cfg.DX, (h-height)/2)
	scouterkit.Flush()
	failed := false
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
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
		return errors.New("the panels do not show the text")
	}
	return nil
}
