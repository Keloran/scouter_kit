//go:build emulator

package scouterkit

import (
	"math"
	"strings"
	"testing"
	"time"
)

// level and frequency (upward zero crossings) of the frames; liblc3 decodes a
// pure tone about 0.7 dB louder.
func measureTone(frames []MicFrame) (dbfs, hz float64) {
	var sum float64
	var n, up int
	prev := int16(0)
	for _, f := range frames {
		for _, v := range f.PCM {
			sum += float64(v) * float64(v)
			if n > 0 && prev < 0 && v >= 0 {
				up++
			}
			prev = v
			n++
		}
	}
	return 10 * math.Log10(sum/float64(n)/(32768*32768)), float64(up) * MicSampleRate / float64(n)
}

func readFrames(t *testing.T, frames <-chan MicFrame, n int) []MicFrame {
	t.Helper()
	var out []MicFrame
	for len(out) < n {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("the channel closed after %d frames", len(out))
			}
			if len(out) > 0 && f.Counter != out[len(out)-1].Counter+1 {
				t.Errorf("frame %d after %d", f.Counter, out[len(out)-1].Counter)
			}
			out = append(out, f)
		case <-time.After(3 * time.Second):
			t.Fatalf("no frame after %d", len(out))
		}
	}
	return out
}

func waitClosed(t *testing.T, frames <-chan MicFrame) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the channel stayed open")
		}
	}
}

func TestMic(t *testing.T) {
	start(t)
	if !Right.State().Microphone || Left.State().Microphone {
		t.Fatalf("microphone: left %v, right %v", Left.State().Microphone, Right.State().Microphone)
	}
	if _, err := Left.MicStart(Mic32kbps); err == nil || !strings.Contains(err.Error(), "mic") {
		t.Errorf("left MicStart: %v", err)
	}
	frames, err := Right.MicStart(Mic32kbps)
	if err != nil {
		t.Fatal(err)
	}
	got := readFrames(t, frames, 120)
	if got[0].Counter != 0 {
		t.Errorf("first counter %d", got[0].Counter)
	}
	for _, f := range got {
		if f.Concealed {
			t.Errorf("frame %d concealed", f.Counter)
		}
	}
	if dbfs, hz := measureTone(got[20:]); math.Abs(dbfs+15.05) > 1 || math.Abs(hz-440) > 2 {
		t.Errorf("440 Hz at -15.05 dBFS: %.2f dBFS, %.1f Hz", dbfs, hz)
	}
	Right.SetEmulatedMic(1000, 0.5)
	readFrames(t, frames, 20)
	if dbfs, hz := measureTone(readFrames(t, frames, 100)); math.Abs(dbfs+9.03) > 1 || math.Abs(hz-1000) > 3 {
		t.Errorf("1 kHz at -9.03 dBFS: %.2f dBFS, %.1f Hz", dbfs, hz)
	}
	s := Right.MicState()
	if !s.On || s.FrameBytes != 40 || s.FramesPerEvent != 5 || s.Gaps != 0 || s.Bad != 0 || s.Frames < 240 {
		t.Errorf("state %+v", s)
	}
	if err := Right.MicStop(); err != nil {
		t.Error(err)
	}
	waitClosed(t, frames)
	if s := Right.MicState(); s.On || s.Stopped != MicStoppedByHost {
		t.Errorf("after MicStop: %+v", s)
	}
}

func TestMicStops(t *testing.T) {
	start(t)
	Right.SetEmulatedMic(440, 0.25)
	frames, err := Right.MicStart(Mic16kbps)
	if err != nil {
		t.Fatal(err)
	}
	if dbfs, _ := measureTone(readFrames(t, frames, 50)[20:]); math.Abs(dbfs+15.05) > 1.5 {
		t.Errorf("16 kbps: %.2f dBFS", dbfs)
	}
	// PANEL = 0 while on (VM.md §6.5): the microphone goes on (the panel shows only the indicator);
	// MicStop then takes the panel off.
	Right.SetPanel(false)
	readFrames(t, frames, 30)
	if s := Right.MicState(); !s.On || s.FrameBytes != 20 {
		t.Errorf("after PANEL = 0: %+v", s)
	}
	if err := Right.MicStop(); err != nil {
		t.Error(err)
	}
	waitClosed(t, frames)
	if s := Right.MicState(); s.On || s.Stopped != MicStoppedByHost {
		t.Errorf("after MicStop: %+v", s)
	}

	if frames, err = Right.MicStart(Mic32kbps); err != nil {
		t.Fatal(err)
	}
	readFrames(t, frames, 10)
	Right.Disconnect(300 * time.Millisecond)
	waitClosed(t, frames)
	if s := Right.MicState(); s.On || s.Stopped != MicStoppedLink {
		t.Errorf("after the link dropped: %+v", s)
	}
	if err := Right.MicStop(); err != nil {
		t.Error(err)
	}
}

func TestKeepSessions(t *testing.T) {
	if err := Init(Config{DX: 100, DY: 9, Left: "none", KeepSessions: true}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(Deinit)
	if _, err := Alloc(64); err == nil {
		t.Error("Alloc in a kept session")
	}
	if _, err := CreateBitmap(8, 8); err == nil || TotalMemory() != 0 {
		t.Errorf("CreateBitmap: %v, total memory %d", err, TotalMemory())
	}
	frames, err := Right.MicStart(Mic32kbps)
	if err != nil {
		t.Fatal(err)
	}
	readFrames(t, frames, 20)
	if err := Right.MicStop(); err != nil {
		t.Error(err)
	}
	waitClosed(t, frames)
}
