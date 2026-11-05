//go:build emulator

package main

import (
	"image"
	"testing"
	"time"

	"scouterkit"
)

func blank(img *image.Gray) bool {
	if img == nil {
		return true
	}
	for _, v := range img.Pix {
		if v != 0 {
			return false
		}
	}
	return true
}

func burst(t *testing.T, arm scouterkit.Arm, d time.Duration) int {
	t.Helper()
	ch, err := arm.MicStart(scouterkit.Mic32kbps)
	if err != nil {
		t.Fatal(err)
	}
	frames := 0
	for end := time.After(d); ; {
		select {
		case _, ok := <-ch:
			if ok {
				frames++
				continue
			}
		case <-end:
		}
		break
	}
	return frames
}

// The notification program and microphone bursts from a new host process that
// keeps the sessions (as micsource does), VM.md §6.5: a popup that the program
// hides during a burst (PANEL = 0) does not end the burst, the panel then shows
// only the indicator and reads 0, and goes off when the microphone stops; a
// second burst with the popup gone; both arms' programs keep running.
func TestMicKeepsProgram(t *testing.T) {
	c := testConfig(t, privateBus(t))
	c.view.standby = true // the hide writes PANEL 2 during the burst
	c.serve = true
	m := start(t, c)
	stop := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- m.loop(stop) }()
	if r, err := viaMirror(c.socket, request{Summary: "Hello", Body: "hidden during the burst", TimeoutMS: 1500}); err != nil || !r.Shown {
		t.Fatalf("%+v, %v", r, err)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before := map[scouterkit.Arm]uint32{}
	for _, arm := range m.g.arms {
		s, err := arm.VMStatus()
		if err != nil || s.State != 1 {
			t.Fatalf("%s arm before: %+v, %v", arm, s, err)
		}
		before[arm] = s.Tasks
	}
	scouterkit.EmulateNewHost(true)
	time.Sleep(300 * time.Millisecond)
	right := scouterkit.Right
	frames := burst(t, right, 4*time.Second) // the popup's 1.5 s end in it: the program writes PANEL 2
	if right.MicState().Stopped != scouterkit.MicStoppedByHost || frames < 350 {
		t.Fatalf("burst over the hide: %d frames, %+v", frames, right.MicState())
	}
	p, err := right.Props(scouterkit.PropPanel)
	if err != nil || p[0] != panelStandby {
		t.Fatalf("PANEL during the burst: %v, %v", p, err)
	}
	if err := right.MicStop(); err != nil {
		t.Fatal(err)
	}
	if !blank(right.EmulatedPanel()) {
		t.Fatal("the panel is not dark after the burst")
	}
	if frames := burst(t, right, 5*time.Second); frames < 450 {
		t.Fatalf("second burst: %d frames, %+v", frames, right.MicState())
	}
	if err := right.MicStop(); err != nil {
		t.Fatal(err)
	}
	for _, arm := range m.g.arms {
		s, err := arm.VMStatus()
		t.Logf("%s arm after: state %d, tasks %d (before %d)", arm, s.State, s.Tasks, before[arm])
		if err != nil || s.State != 1 {
			t.Fatalf("%s arm after the bursts: %+v, %v", arm, s, err)
		}
	}
}
