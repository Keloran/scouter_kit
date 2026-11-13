package main

import (
	"context"
	"errors"
	"fmt"
	"image"
	"os"
	"os/signal"
	"syscall"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

const calibrationW, calibrationH = 560, 360

const calibrationPosition = 3840

// Interactive controls and save confirmations are command output, not debug logs.
func calibrationFeedback(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

type calibrationView struct {
	base          uint32
	dx, dy, x, y  int
	font, numbers *scouterkit.Font
	bitmaps       map[scouterkit.Arm]*scouterkit.Bitmap
	scale         float64
}

func connectDisplay(f demo.Flags, o options) (scouterkit.Config, []scouterkit.Arm, error) {
	return connectDisplayContext(context.Background(), f, o, nil)
}

func connectDisplayContext(ctx context.Context, f demo.Flags, o options, cancellation *scouterkit.Cancellation) (scouterkit.Config, []scouterkit.Arm, error) {
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter, cfg.Log = f.Left, f.Right, f.Adapter, f.Log
	cfg.DX, cfg.DY = o.dx, o.dy
	cfg.Cancellation = cancellation
	if err := scouterkit.InitContext(ctx, cfg); err != nil {
		return cfg, nil, err
	}
	return cfg, demo.Present(), nil
}

// The ruler belongs only to the left eye, so there is one unambiguous set
// of coordinates against which to read the right eye's crosshair.
func calibrationBitmap(font, numbers *scouterkit.Font, arm scouterkit.Arm, major int) (*scouterkit.Bitmap, error) {
	b, err := scouterkit.CreateBitmap(calibrationW, calibrationH)
	if err != nil {
		return nil, err
	}
	paintCalibrationBitmap(b, font, numbers, arm, major)
	return b, nil
}

func paintCalibrationBitmap(b *scouterkit.Bitmap, font, numbers *scouterkit.Font, arm scouterkit.Arm, major int) {
	b.Fill(b.Bounds(), 0)
	cx, cy := calibrationW/2, calibrationH/2
	line := func(x, y, w, h int, gray uint8) { b.Fill(image.Rect(x, y, x+w, y+h), gray) }
	text := func(s string, x, y int, align scouterkit.Align) {
		font.DrawLine(b, s, x, y, align, 204)
	}
	number := func(s string, x, y int, align scouterkit.Align) {
		numbers.DrawLine(b, s, x, y, align, 255)
	}
	if arm == scouterkit.Left {
		text("LEFT: pixel ruler", cx, 18, scouterkit.AlignCenter)
		text("X: right + / left -    Y: down + / up -", cx, 34, scouterkit.AlignCenter)
		for y := -120; y <= 120; y += 10 {
			for x := -240; x <= 240; x += 10 {
				line(cx+x, cy+y, 1, 1, 102)
			}
		}
		line(cx-240, cy, 481, 1, 153)
		line(cx, cy-120, 1, 241, 153)
		for x := -240; x <= 240; x += 10 {
			n := 3
			if x%major == 0 {
				n = 7
				if x != 0 {
					number(fmt.Sprintf("%+d", x), cx+x, cy+12+numbers.Ascent, scouterkit.AlignCenter)
				}
			}
			line(cx+x, cy-n, 1, 2*n+1, 204)
		}
		for y := -120; y <= 120; y += 10 {
			n := 3
			if y%major == 0 {
				n = 7
				if y != 0 {
					number(fmt.Sprintf("%+d", y), cx+12, cy+y+numbers.Ascent/2, scouterkit.AlignLeft)
				}
			}
			line(cx-n, cy+y, 2*n+1, 1, 204)
		}
		// Hollow square at the left eye's origin; the right plus should fit inside.
		line(cx-5, cy-5, 11, 1, 255)
		line(cx-5, cy+5, 11, 1, 255)
		line(cx-5, cy-5, 1, 11, 255)
		line(cx+5, cy-5, 1, 11, 255)
		text("L 0,0", cx-12, cy-13, scouterkit.AlignRight)
		text(fmt.Sprintf("Small marks: 10 px / numbers: %d px", major), cx, calibrationH-8, scouterkit.AlignCenter)
	} else {
		text("RIGHT: crosshair", cx, 18, scouterkit.AlignCenter)
		line(cx-18, cy, 37, 1, 255)
		line(cx, cy-18, 1, 37, 255)
		text("R", cx+24, cy-13, scouterkit.AlignLeft)
	}
	number("0123456789", cx, calibrationH-30, scouterkit.AlignCenter)
}

func calibrationProgram(base uint32, b *scouterkit.Bitmap) *scouterkit.Program {
	p := scouterkit.NewProgramAt(base, 32)
	show := p.Label()
	p.PanelPolicy(show)
	p.Bind(show)
	c := p.Const
	n := func(v int) scouterkit.Word { return c(uint32(int32(v))) }
	addr, stride := b.Address()
	position := scouterkit.Word((base + calibrationPosition) / 4)
	p.Draw(c(addr), n(stride), c(0), c(0), position, position+1, n(calibrationW), n(calibrationH))
	p.Notify(c(1), c(0))
	p.End()
	p.On(scouterkit.TriggerStart, show)
	p.On(scouterkit.TriggerUser0, show)
	return p
}

func showCalibration(cfg scouterkit.Config, arms []scouterkit.Arm, scale float64) (*calibrationView, error) {
	if err := (displaySettings{FontScale: scale}).validate(); err != nil {
		return nil, err
	}
	font, err := scouterkit.LoadFont(scouterkit.DepartureMono(), "", 1)
	if err != nil {
		return nil, err
	}
	numbers := font
	if scale != 1 {
		numbers, err = loadDisplayFont("+-0123456789", scale)
		if err != nil {
			return nil, err
		}
	}
	major := 40
	if scale > 1 {
		major = 80
	}
	base, err := scouterkit.Alloc(4096)
	if err != nil {
		return nil, err
	}
	v := &calibrationView{base: base, dx: cfg.DX, dy: cfg.DY, font: font, numbers: numbers,
		bitmaps: make(map[scouterkit.Arm]*scouterkit.Bitmap), scale: scale}
	vw, vh := scouterkit.Size()
	for _, arm := range arms {
		b, err := calibrationBitmap(font, numbers, arm, major)
		if err != nil {
			return nil, err
		}
		v.bitmaps[arm] = b
		x, y := demo.PanelAt(cfg, arm, (vw-calibrationW)/2, (vh-calibrationH)/2)
		p := calibrationProgram(base, b)
		if p.Here() > base+calibrationPosition {
			return nil, errors.New("calibration program overlaps its position mailbox")
		}
		arm.WriteArena(base+calibrationPosition, demo.Words(uint32(int32(x)), uint32(int32(y))))
		if err := arm.Load(p); err != nil {
			return nil, err
		}
		if err := arm.Start(base); err != nil {
			return nil, err
		}
		if _, err := demo.WaitNotify(arm, 5*time.Second); err != nil {
			return nil, err
		}
		if arm == scouterkit.Right {
			v.x, v.y = x, y
		}
		progress("%s calibration pattern ready", arm)
	}
	return v, nil
}

func (v *calibrationView) move(dx, dy int) error {
	// Hold the left ruler fixed. DX moves the right image left; DY moves it down.
	x, y := v.x+v.dx-dx, v.y+dy-v.dy
	scouterkit.Right.WriteArena(v.base+calibrationPosition, demo.Words(uint32(int32(x)), uint32(int32(y))))
	scouterkit.Right.Raise(scouterkit.TriggerUser0)
	if _, err := demo.WaitNotify(scouterkit.Right, 5*time.Second); err != nil {
		return err
	}
	v.dx, v.dy, v.x, v.y = dx, dy, x, y
	return nil
}

func (v *calibrationView) resize(scale float64) error {
	if scale == v.scale {
		return nil
	}
	if err := (displaySettings{FontScale: scale}).validate(); err != nil {
		return err
	}
	numbers := v.font
	if scale != 1 {
		var err error
		numbers, err = loadDisplayFont("+-0123456789", scale)
		if err != nil {
			return err
		}
	}
	if v.numbers != v.font {
		v.numbers.Unload()
	}
	v.numbers, v.scale = numbers, scale
	major := 40
	if scale > 1 {
		major = 80
	}
	for _, arm := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		paintCalibrationBitmap(v.bitmaps[arm], v.font, numbers, arm, major)
		arm.Raise(scouterkit.TriggerUser0)
		if _, err := demo.WaitNotify(arm, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}

func calibrate(f demo.Flags, o options) error {
	if o.preview || f.Left == "none" || f.Right == "none" || o.stay < 0 {
		return errors.New("calibration needs both arms, no -preview, and -for >= 0")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if o.stay > 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithTimeout(ctx, o.stay)
		defer stop()
	}
	progress("Calibration: looking for both glasses arms")
	cfg, arms, err := connectDisplay(f, o)
	if err != nil {
		return err
	}
	defer func() {
		demo.Stop(arms)
		scouterkit.Deinit()
		progress("Calibration stopped; both panels off")
	}()
	if ctx.Err() != nil {
		return nil
	}
	if len(arms) != 2 {
		return errors.New("calibration needs both glasses arms connected")
	}
	v, err := showCalibration(cfg, arms, o.fontScale)
	if err != nil {
		return err
	}
	keys, restore, err := calibrationKeyboard(ctx, os.Stdin)
	if err != nil {
		return err
	}
	defer restore()
	calibrationFeedback("Calibration ready (dx=%d, dy=%d, font scale=%g). Small marks=10px; numbered spacing shown on ruler. Ctrl-C stops.", cfg.DX, cfg.DY, o.fontScale)
	if keys == nil {
		calibrationFeedback("No terminal input; run -calibrate in a terminal for arrow keys and S to save")
	} else {
		calibrationFeedback("Arrows move RIGHT crosshair by 1 pixel; Page Up/Down change font size by 0.1 (0.1–3); S saves; Q or Ctrl-C exits. Changes are saved only with S.")
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case input, ok := <-keys:
			if !ok {
				return nil
			}
			if input.err != nil {
				return input.err
			}
			switch input.key {
			case calibrationQuit:
				return nil
			case calibrationSave:
				if err := saveDisplaySettings(o); err != nil {
					calibrationFeedback("Could not save calibration: %v", err)
				} else {
					calibrationFeedback("Saved: dx=%d, dy=%d, font scale=%g", o.dx, o.dy, o.fontScale)
				}
			case calibrationLarger, calibrationSmaller:
				next := nudgeCalibration(o, input.key)
				if next.fontScale == o.fontScale {
					calibrationFeedback("Font size is at its limit (%g)", o.fontScale)
					continue
				}
				if err := v.resize(next.fontScale); err != nil {
					return err
				}
				o = next
				calibrationFeedback("dx=%d, dy=%d, font scale=%g (unsaved; S to save)", o.dx, o.dy, o.fontScale)
			default:
				next := nudgeCalibration(o, input.key)
				if err := (displaySettings{DX: next.dx, DY: next.dy, FontScale: next.fontScale}).validate(); err != nil {
					calibrationFeedback("Alignment limit reached: %v", err)
					continue
				}
				if err := v.move(next.dx, next.dy); err != nil {
					return err
				}
				o = next
				calibrationFeedback("dx=%d, dy=%d (unsaved; S to save)", o.dx, o.dy)
			}
		}
	}
}
