//go:build emulator

package main

import (
	"context"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func lit(img *image.Gray, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if img.GrayAt(x, y).Y != 0 {
				n++
			}
		}
	}
	return n
}

func TestStandbyShowsNotConnectedAndHostRepaintsAfter(t *testing.T) {
	c, err := newClient("https://example.com", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c.http.Transport = systemTestTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"projects":1,"customers":2,"paid_customers":1}`
		if r.URL.Path == "/api/projects" {
			body = `[{"id":"one","slug":"one","name":"Site with a long name","stage":"launched"}]`
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- display(ctx, demo.Flags{}, options{dx: -10, dy: -8, fontScale: 1.5, refresh: 5 * time.Minute, aiRefresh: 30 * time.Second, rotate: 0}, c)
	}()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("dashboard: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("dashboard did not stop")
		}
	}()
	arm := scouterkit.Right
	time.Sleep(1500 * time.Millisecond) // the dashboard is up
	before := arm.EmulatedPanel()
	arm.Disconnect(4 * time.Second)
	time.Sleep(3 * time.Second) // one link check (every 2 s) sees LINK 0
	offline := arm.EmulatedPanel()
	if out := os.Getenv("STANDBY_PNG"); out != "" {
		for name, img := range map[string]*image.Gray{"before": before, "offline": offline} {
			f, _ := os.Create(out + "-" + name + ".png")
			png.Encode(f, img)
			f.Close()
		}
	}
	all := offline.Bounds()
	if lit(offline, all) == 0 || lit(offline, all) >= lit(before, all) {
		t.Fatalf("offline face not drawn: %d lit pixels, %d before", lit(offline, all), lit(before, all))
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, want, err := arm.PanelCRC(0, scouterkit.PanelHeight)
		if err == nil && got == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host did not repaint after the link came back (crc %08x, want %08x, %v)", got, want, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func TestStandbyProgramWithPeerLinkFits(t *testing.T) {
	if err := scouterkit.Init(scouterkit.DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	const base = 0x20000
	b := band{w: 80, h: 20, x: 500, y: 90, px: 2, baseline: 16, addr: base + standbyCode + standbyState}
	for _, peerY := range []int{-1, 110} {
		f := standbyFace{msg: b, batt: b, clock: b, own: battRow{1, 90}, peer: battRow{2, peerY}, font: base + 0x8000,
			msgLen: 13, zero: 3, space: 0, dash: 2, colon: 13}
		p := standbyProgram(base, base+standbyCode, f)
		if p.Here() > base+standbyCode {
			t.Fatalf("peer row %d: program of %d bytes exceeds %d", peerY, p.Here()-base, standbyCode)
		}
	}
}

func TestStandbyClockTicksAndWraps(t *testing.T) {
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	defer scouterkit.Deinit()
	s, err := newScreenScale(cfg, 1.5)
	if err != nil {
		t.Fatal(err)
	}
	arms := []scouterkit.Arm{scouterkit.Left, scouterkit.Right}
	state, err := installStandby(cfg, s, 1.5, arms)
	if err != nil {
		t.Fatal(err)
	}
	minute := func(arm scouterkit.Arm, want uint32) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			b, err := arm.ReadArena(state+4*wMinute, 4)
			if err == nil && uint32(b[0])|uint32(b[1])<<8 == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s arm: minute %v, want %d (%v)", arm, b, want, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	for _, m := range []uint32{59, 1439} {
		for _, arm := range arms {
			arm.WriteArena(state+4*wMailMin, demo.Words(m, arm.DeviceTime(time.Second)))
			arm.Raise(scouterkit.TriggerUser0)
			minute(arm, m)
			minute(arm, (m+1)%1440) // a link check after the next minute's start
		}
	}
}
