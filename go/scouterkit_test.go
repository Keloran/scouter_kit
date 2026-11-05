//go:build emulator

package scouterkit

import (
	"bytes"
	"errors"
	"image"
	"strings"
	"sync"
	"testing"
	"time"
)

func start(t *testing.T) {
	t.Helper()
	if err := Init(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Deinit)
}

func TestBeforeInit(t *testing.T) {
	if w, h := Size(); w != 0 || h != 0 {
		t.Errorf("size %dx%d", w, h)
	}
	if _, err := CreateBitmap(8, 8); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("CreateBitmap: %v", err)
	}
	if _, err := LoadFont(DepartureMono(), "", 1); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("LoadFont: %v", err)
	}
	if _, _, err := Left.PanelCRC(0, PanelHeight); !errors.Is(err, ErrNoReply) {
		t.Errorf("PanelCRC: %v", err)
	}
	Clear()
	Flush()
}

func TestInit(t *testing.T) {
	start(t)
	if w, h := Size(); w != 740 || h != 471 {
		t.Errorf("size %dx%d, want 740x471", w, h)
	}
	if err := Init(DefaultConfig()); !errors.Is(err, ErrBluetooth) {
		t.Errorf("second Init: %v", err)
	}
	for _, arm := range []Arm{Left, Right} {
		s := arm.State()
		if !s.Present || !s.Connected || !strings.HasPrefix(s.Firmware, "0.5.") || s.Errors != 0 {
			t.Errorf("%s arm: %+v", arm, s)
		}
		if err := arm.SetBrightness(20); err != nil {
			t.Errorf("%s arm: SetBrightness: %v", arm, err)
		}
		if _, err := arm.Battery(); err != nil {
			t.Errorf("%s arm: Battery: %v", arm, err)
		}
	}
	if err := Left.SetBrightness(43); err == nil {
		t.Error("brightness 43 accepted")
	}
}

// The emulated arms sit behind one emulated adapter, hci0.
func TestAdapter(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Adapter = "hci7"
	if err := Init(cfg); !errors.Is(err, ErrBluetooth) || !strings.Contains(err.Error(), "no Bluetooth adapter hci7") {
		t.Errorf("Init on hci7: %v", err)
	}
	cfg.Adapter = "hci0"
	if err := Init(cfg); err != nil {
		t.Fatalf("Init on hci0: %v", err)
	}
	Deinit()
}

// Program.PanelPolicy: the panel off in the case and while folded, the
// program's DRAW showing nothing meanwhile; then its redraw label draws.
func TestPanelPolicy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Left = "none"
	if err := Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Deinit)
	b, err := CreateBitmap(64, 64)
	if err != nil {
		t.Fatal(err)
	}
	b.Fill(image.Rect(0, 0, 64, 64), 255)
	Flush()
	addr, stride := b.Address()
	base, err := Alloc(512)
	if err != nil {
		t.Fatal(err)
	}
	p := NewProgramAt(base, 16)
	c := p.Const
	draw := p.Label()
	p.PanelPolicy(draw)
	p.Bind(draw)
	p.Draw(c(addr), c(uint32(stride)), c(0), c(0), c(300), c(100), c(64), c(64))
	p.End()
	p.On(TriggerStart, draw)
	p.On(TriggerUser0, draw)
	if err := Right.Load(p); err != nil {
		t.Fatal(err)
	}
	if err := Right.Start(base); err != nil {
		t.Fatal(err)
	}
	shown := func(want bool, what string) {
		t.Helper()
		for deadline := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			if bytes.Contains(Right.EmulatedPanel().Pix, []byte{255}) == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the square shown %v", what, !want)
			}
		}
	}
	shown(true, "start")
	Right.SetEmulatedProperty(PropInCase, 1)
	shown(false, "in the case")
	Right.Raise(TriggerUser0)
	time.Sleep(200 * time.Millisecond)
	shown(false, "a DRAW in the case")
	Right.SetEmulatedProperty(PropInCase, 0)
	shown(true, "out of the case")
	Right.SetEmulatedProperty(PropWorn, 0)
	shown(false, "folded")
	Right.SetEmulatedProperty(PropWorn, 1)
	shown(true, "unfolded")
}

func TestLoadBitmapLevels(t *testing.T) {
	start(t)
	ramp := image.NewGray(image.Rect(0, 0, 256, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 256; x++ {
			ramp.Pix[y*ramp.Stride+x] = uint8(x)
		}
	}
	b, err := LoadBitmap(ramp)
	if err != nil {
		t.Fatal(err)
	}
	got := b.Image()
	if got.Bounds() != ramp.Bounds() {
		t.Fatalf("bounds %v", got.Bounds())
	}
	for _, v := range got.Pix {
		if v%17 != 0 {
			t.Fatalf("pixel %d is not one of the 16 levels", v)
		}
	}
	// Floyd–Steinberg keeps the brightness of every 16-column band.
	for x0 := 0; x0 < 256; x0 += 16 {
		sum, want := 0, 0
		for y := 0; y < 32; y++ {
			for x := x0; x < x0+16; x++ {
				sum += int(got.Pix[y*got.Stride+x])
				want += x
			}
		}
		if d := (sum - want) / (16 * 32); d < -4 || d > 4 {
			t.Errorf("columns %d...%d: mean off by %d", x0, x0+15, d)
		}
	}

	exact := image.NewGray(image.Rect(0, 0, 16, 4))
	for i := range exact.Pix {
		exact.Pix[i] = uint8(i % 16 * 17)
	}
	b, err = LoadBitmap(exact)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.Image(); string(got.Pix) != string(exact.Pix) {
		t.Errorf("levels changed: %v", got.Pix)
	}
}

func TestResponse(t *testing.T) {
	start(t)
	t.Cleanup(func() { SetResponse(GammaResponse(1)) })
	SetResponse(GammaResponse(2.2))
	if r := Response(); r[128] != 56 {
		t.Fatalf("response[128] = %d", r[128])
	}
	flat := image.NewGray(image.Rect(0, 0, 64, 64))
	for i := range flat.Pix {
		flat.Pix[i] = 128
	}
	b, err := LoadBitmap(flat)
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, v := range b.Image().Pix {
		sum += int(v)
	}
	if mean := sum / len(flat.Pix); mean < 54 || mean > 58 {
		t.Errorf("mean %d, want about 56", mean)
	}
}

func TestEndToEnd(t *testing.T) {
	start(t)
	font, err := LoadFont(DepartureMono(), "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if font.Ascent != 22 || font.Descent != 6 || font.LineSpacing != 28 {
		t.Errorf("metrics %d %d %d", font.Ascent, font.Descent, font.LineSpacing)
	}
	text := "Hello from Go"
	if e := font.Measure(text); e.Width != 13*14 || e.Height <= 0 {
		t.Errorf("measure %+v", e)
	}
	line, err := CreateBitmap(600, 32)
	if err != nil {
		t.Fatal(err)
	}
	if w := font.DrawLine(line, text, 300, 24, AlignCenter, 255); w != 13*14 {
		t.Errorf("advance %d", w)
	}
	pic := image.NewGray(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			pic.Pix[y*pic.Stride+x] = uint8(x + y*x%56)
		}
	}
	b, err := LoadBitmap(pic)
	if err != nil {
		t.Fatal(err)
	}
	Draw(line, 70, 200)
	Draw(b, 500, 300)
	Flush()
	display := Display()
	sameAt(t, display, line.Image(), 70, 200)
	sameAt(t, display, b.Image(), 500, 300)
	lit := 0
	for _, v := range line.Image().Pix {
		if v != 0 {
			lit++
		}
	}
	if lit < 100 {
		t.Errorf("only %d lit pixels in the text", lit)
	}
	checkPanels(t)

	line.Fill(line.Bounds(), 0)
	font.DrawLine(line, "Second update", 0, 24, AlignLeft, 136)
	b.Fill(image.Rect(150, 50, 400, 400), 255)
	Draw(line, 70, 200)
	Draw(b, 500, 300)
	Flush()
	checkPanels(t)
}

func sameAt(t *testing.T, display, img *image.Gray, x0, y0 int) {
	t.Helper()
	r := img.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if got, want := display.GrayAt(x0+x, y0+y).Y, img.GrayAt(x, y).Y; got != want {
				t.Fatalf("display (%d, %d) = %d, want %d", x0+x, y0+y, got, want)
			}
		}
	}
}

// checkPanels reads both panels back and compares the emulated panels with
// the display: the left eye shows columns 0...639 on its rows 0...470, the
// right eye columns 100...739 on its rows 9...479.
func checkPanels(t *testing.T) {
	t.Helper()
	display := Display()
	for _, arm := range []Arm{Left, Right} {
		got, want, err := arm.PanelCRC(0, PanelHeight)
		if err != nil || got != want {
			t.Errorf("%s panel: crc %08x, want %08x, %v", arm, got, want, err)
		}
		ox, oy := 0, 0
		if arm == Right {
			ox, oy = 100, 9
		}
		panel := arm.EmulatedPanel()
		wrong := 0
		for py := 0; py < PanelHeight; py++ {
			for px := 0; px < PanelWidth; px++ {
				want := uint8(0)
				if y := py - oy; y >= 0 && y < display.Rect.Dy() {
					want = display.GrayAt(px+ox, y).Y
				}
				if panel.GrayAt(px, py).Y != want {
					wrong++
				}
			}
		}
		if wrong > 0 {
			t.Errorf("%s panel: %d pixels differ from the display", arm, wrong)
		}
	}
}

func TestHandles(t *testing.T) {
	start(t)
	total := TotalMemory()
	b, err := CreateBitmap(100, 100)
	if err != nil {
		t.Fatal(err)
	}
	font, err := LoadFont(DepartureMono(), "abc", 1)
	if err != nil {
		t.Fatal(err)
	}
	if FreeMemory() >= total {
		t.Errorf("free %d of %d", FreeMemory(), total)
	}
	b.Unload()
	b.Unload()
	Draw(b, 0, 0)
	b.Fill(b.Bounds(), 255)
	if b.Image() != nil {
		t.Error("an unloaded bitmap has pixels")
	}
	other, err := CreateBitmap(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if w := font.DrawLine(b, "abc", 0, 10, AlignLeft, 255); w != 0 {
		t.Errorf("drew %d px into an unloaded bitmap", w)
	}
	UnloadAll()
	if FreeMemory() != total {
		t.Errorf("free %d of %d after UnloadAll", FreeMemory(), total)
	}
	if other.Image() != nil || font.Measure("abc").Width != 0 {
		t.Error("handles survived UnloadAll")
	}
	Flush()

	if _, err := LoadFont([]byte("bad"), "", 1); err == nil {
		t.Error("a bad font blob loaded")
	}
	if _, err := CreateBitmap(4000, 4000); err == nil || !strings.Contains(err.Error(), "no room") {
		t.Errorf("huge bitmap: %v", err)
	}
	if _, err := CreateBitmap(0, 5); err == nil {
		t.Error("empty bitmap created")
	}
}

func TestDeinitInvalidatesHandles(t *testing.T) {
	if err := Init(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	b, err := CreateBitmap(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	Deinit()
	Draw(b, 0, 0)
	Flush()
	start(t)
	if b.Image() != nil {
		t.Error("a bitmap survived Deinit")
	}
}

func TestConcurrentUse(t *testing.T) {
	start(t)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := CreateBitmap(50, 50)
			if err != nil {
				t.Error(err)
				return
			}
			for k := 0; k < 20; k++ {
				b.Fill(b.Bounds(), uint8(k*13))
				Draw(b, i*60, 100)
				Flush()
				Display()
			}
			b.Unload()
		}()
	}
	wg.Wait()
	Flush()
	checkPanels(t)
}

// The asset store (VM.md §6.10) on the emulated arms: Store, Assets, a stored
// font, Forget.
func TestAssets(t *testing.T) {
	start(t)
	blob := bytes.Repeat([]byte("asset "), 700)
	id := AssetIDOf(blob)
	if err := Store(blob, AssetRaw); err != nil {
		t.Fatal(err)
	}
	for _, arm := range []Arm{Left, Right} {
		list, err := Assets(arm)
		if err != nil || len(list) != 1 || list[0].ID != id || list[0].Size != uint32(len(blob)) || list[0].Type != AssetRaw {
			t.Errorf("%v arm: %+v %v", arm, list, err)
		}
	}
	if err := Store(blob, AssetRaw); err != nil {
		t.Errorf("again: %v", err)
	}
	f, err := LoadFontStored(DepartureMono(), "stored", 1)
	if err != nil {
		t.Fatal(err)
	}
	f.Unload()
	if err := Forget(id); err != nil {
		t.Fatal(err)
	}
	if list, _ := Assets(Right); len(list) > 1 || (len(list) == 1 && list[0].ID == id) {
		t.Errorf("after Forget: %+v", list)
	}
}
