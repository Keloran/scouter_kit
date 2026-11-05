//go:build emulator

package main

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"testing"
	"time"

	"scouterkit"
)

func connect(t *testing.T, cfg scouterkit.Config) {
	t.Helper()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
}

func testOptions(name string) options {
	return options{arm: scouterkit.Right, buffer: 60 * time.Millisecond, duration: 3 * time.Second, hold: 2 * time.Second,
		check: true, name: name, description: "G1 microphone test"}
}

// nodeListed says whether PipeWire lists a node of that name (false without pw-cli).
func nodeListed(name string) bool {
	out, err := exec.Command("pw-cli", "ls", "Node").Output()
	return err == nil && strings.Contains(string(out), `node.name = "`+name+`"`)
}

func runOrSkip(t *testing.T, ctx context.Context, o options) (result, string, error) {
	t.Helper()
	var out bytes.Buffer
	res, err := run(ctx, o, &out)
	if err != nil && strings.HasPrefix(err.Error(), "PipeWire") {
		t.Skip(err)
	}
	return res, out.String(), err
}

// A 1 kHz tone at a quarter of full scale, through the LC3 frames of the
// emulated arm, the library's decoder, the node and a PipeWire capture.
func TestTone(t *testing.T) {
	connect(t, scouterkit.Config{DX: 100, DY: 9, Left: "none"})
	scouterkit.Right.SetEmulatedMic(1000, 0.25)
	name := "scouterkit-g1-mic-test"
	listed := make(chan bool, 1)
	go func() {
		time.Sleep(1500 * time.Millisecond)
		listed <- nodeListed(name)
	}()
	res, out, err := runOrSkip(t, context.Background(), testOptions(name))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if hz := res.recorded.hz(); math.Abs(hz-1000) > 5 {
		t.Errorf("recorded %.1f Hz, want 1000", hz)
	}
	// -15.05 dBFS, and liblc3 decodes a pure tone about 0.7 dB louder.
	if db := res.recorded.dbfs(); math.Abs(db+14.35) > 1 {
		t.Errorf("recorded %.2f dBFS", db)
	}
	if db := res.decoded.dbfs(); math.Abs(db+14.35) > 1 {
		t.Errorf("decoded %.2f dBFS", db)
	}
	if math.Abs(res.perSecond-100) > 0.5 || res.mic.Gaps != 0 || res.node.Underruns != 0 || res.nodeID == 0 {
		t.Errorf("%.2f frames/s, %d gaps, %d underruns, node %d", res.perSecond, res.mic.Gaps, res.node.Underruns,
			res.nodeID)
	}
	if _, err := exec.LookPath("pw-cli"); err == nil {
		if !<-listed {
			t.Error("the node was not listed while it ran")
		}
		if nodeListed(name) {
			t.Error("the node is still there")
		}
	}
	if s := scouterkit.Right.MicState(); s.On {
		t.Error("the microphone is still on")
	}
}

// The link drops while it runs: the microphone stops by itself, run says why
// and removes the node.
func TestLinkDrop(t *testing.T) {
	connect(t, scouterkit.Config{DX: 100, DY: 9, Left: "none"})
	go func() {
		time.Sleep(time.Second)
		scouterkit.Right.Disconnect(300 * time.Millisecond)
	}()
	res, out, err := runOrSkip(t, context.Background(), testOptions("scouterkit-g1-mic-drop"))
	if err == nil || !res.stoppedByItself || res.mic.Stopped != scouterkit.MicStoppedLink {
		t.Errorf("err %v, stopped by itself %v (%v)\n%s", err, res.stoppedByItself, res.mic.Stopped, out)
	}
	if !strings.Contains(out, "node scouterkit-g1-mic-drop removed") {
		t.Errorf("output:\n%s", out)
	}
}

// The microphone is on only while something records from the node, and off
// again a hold time after the recording ends.
func TestOnDemand(t *testing.T) {
	connect(t, scouterkit.Config{DX: 100, DY: 9, Left: "none"})
	name := "scouterkit-g1-mic-demand"
	o := testOptions(name)
	o.check, o.duration, o.hold = false, 4*time.Second, 500*time.Millisecond
	seen := make(chan string, 3)
	go func() {
		time.Sleep(800 * time.Millisecond)
		seen <- fmt.Sprint("before: ", scouterkit.Right.MicState().On)
		c, err := newCapture(name, scouterkit.MicSampleRate)
		if err != nil {
			seen <- err.Error()
			return
		}
		time.Sleep(time.Second)
		seen <- fmt.Sprint("recording: ", scouterkit.Right.MicState().On)
		c.Close()
		time.Sleep(time.Second)
		seen <- fmt.Sprint("after: ", scouterkit.Right.MicState().On)
	}()
	res, out, err := runOrSkip(t, context.Background(), o)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{"before: false", "recording: true", "after: false"} {
		if got := <-seen; got != want {
			t.Errorf("%s, want %s\n%s", got, want, out)
		}
	}
	if res.sessions != 1 || res.frames < 50 {
		t.Errorf("%d times on, %d frames\n%s", res.sessions, res.frames, out)
	}
}

// A program on the right arm (a 100 ms TIMER that counts) runs on while a new
// host keeps the session and records through the node, as micsource does.
func TestKeepsProgram(t *testing.T) {
	connect(t, scouterkit.Config{DX: 100, DY: 9, Left: "none"})
	base, err := scouterkit.Alloc(512)
	if err != nil {
		t.Fatal(err)
	}
	count := scouterkit.Word((base + 508) / 4)
	p := scouterkit.NewProgramAt(base, 16)
	tick := p.Label()
	p.Bind(tick)
	p.Add(count, count, p.Const(1))
	p.SysRead(scouterkit.R(0), p.Const(uint32(scouterkit.PropTimeUS)))
	p.Add(scouterkit.R(0), scouterkit.R(0), p.Const(100000))
	p.SysWrite(p.Const(uint32(scouterkit.PropTimer)), scouterkit.R(0))
	p.End()
	p.On(scouterkit.TriggerStart, tick)
	p.On(scouterkit.TriggerTimer, tick)
	p.Changes(base+508, 4)
	if err := scouterkit.Right.Load(p); err != nil {
		t.Fatal(err)
	}
	if err := scouterkit.Right.Start(base); err != nil {
		t.Fatal(err)
	}
	ticks := func() uint32 {
		b, err := scouterkit.Right.ReadArena(base+508, 4)
		if err != nil {
			t.Fatal(err)
		}
		return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
	}
	time.Sleep(300 * time.Millisecond)
	before, epoch := ticks(), scouterkit.Right.State().Epoch
	reconnects := scouterkit.Right.State().Reconnects
	scouterkit.EmulateNewHost(true)
	for k := 0; k < 100 && !(scouterkit.Right.State().Reconnects > reconnects && scouterkit.Right.State().Connected); k++ {
		time.Sleep(50 * time.Millisecond)
	}
	res, out, err := runOrSkip(t, context.Background(), testOptions("scouterkit-g1-mic-keep"))
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s, err := scouterkit.Right.VMStatus()
	after := ticks()
	if err != nil || s.State != 1 || after < before+25 || scouterkit.Right.State().Epoch != epoch {
		t.Errorf("program %+v (%v), ticks %d then %d, epoch %08x then %08x", s, err, before, after, epoch,
			scouterkit.Right.State().Epoch)
	}
	if res.mic.Gaps != 0 || res.indicatorOn != nil || res.indicatorOff != nil {
		t.Errorf("%+v", res.mic)
	}
}

// An arm without capture (HELLO.board bit 2 clear) is refused before anything
// starts; so is a run cancelled before the microphone is on.
func TestRefused(t *testing.T) {
	connect(t, scouterkit.DefaultConfig())
	o := testOptions("scouterkit-g1-mic-left")
	o.arm = scouterkit.Left
	if _, out, err := runOrSkip(t, context.Background(), o); err == nil ||
		!strings.Contains(err.Error(), "no microphone capture") || out != "" {
		t.Errorf("left arm: %v\n%s", err, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, out, err := runOrSkip(t, ctx, testOptions("scouterkit-g1-mic-cancelled")); err == nil || out != "" {
		t.Errorf("cancelled: %v\n%s", err, out)
	}
	if scouterkit.Right.MicState().On {
		t.Error("the microphone is on")
	}
}
