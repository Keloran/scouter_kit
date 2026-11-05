// Command micsource makes the glasses' microphone a PipeWire audio source,
// "G1 glasses microphone" (node scouterkit-g1-mic, 16 kHz mono S16), that any
// PipeWire, PulseAudio or JACK application can record from. Only the arm with
// the microphone is connected, and the microphone is on only while something
// records from the node. Once a second it shows the microphone and
// indicator state, the stream's health and the level. On exit, Ctrl-C,
// SIGTERM and SIGHUP it switches the microphone off and removes the node. It
// refuses an arm whose firmware has no capture (HELLO.board bit 2). It never
// writes audio anywhere but to the node.
//
//	go run ./examples/micsource [-arm right] [-rate 32] [-buffer 60ms] [-hold 2s] [-for D] [-check]
//	pw-record --target scouterkit-g1-mic out.wav     # in another terminal
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"io"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type options struct {
	arm               scouterkit.Arm
	rate              scouterkit.MicRate
	buffer, duration  time.Duration
	hold              time.Duration
	check             bool
	name, description string
}

func main() {
	var f demo.Flags
	f.Register()
	var o options
	arm := flag.String("arm", "right", "the arm whose microphone to use (on the G1 only the right arm captures)")
	kbps := flag.Int("rate", 32, "LC3 bitrate on the link, 32 or 16 kbps")
	flag.DurationVar(&o.buffer, "buffer", 60*time.Millisecond, "audio kept queued for PipeWire: the latency the node adds")
	flag.DurationVar(&o.duration, "for", 0, "stop after this long (0: at Ctrl-C; with -check: 5s)")
	flag.DurationVar(&o.hold, "hold", 2*time.Second, "the microphone stays on this long after the last recording from the node ends")
	flag.BoolVar(&o.check, "check", false, "record from the node through PipeWire (in memory), read the indicator back and check the stream")
	flag.StringVar(&o.name, "name", "scouterkit-g1-mic", "PipeWire node name")
	flag.StringVar(&o.description, "description", "G1 glasses microphone", "PipeWire node description")
	demo.Usage("micsource [-arm right] [-rate 32|16] [-buffer 60ms] [-hold 2s] [-for D] [-check] [-right ADDR] [-adapter hci1]")
	flag.Parse()
	switch {
	case *arm != "right" && *arm != "left":
		fail(fmt.Errorf("-arm %q: right or left", *arm))
	case *kbps != 32 && *kbps != 16:
		fail(fmt.Errorf("-rate %d: 32 or 16", *kbps))
	case o.buffer < 20*time.Millisecond || o.buffer > 500*time.Millisecond:
		fail(fmt.Errorf("-buffer %v: 20ms...500ms", o.buffer))
	}
	if *arm == "left" {
		o.arm = scouterkit.Left
	} else {
		o.arm = scouterkit.Right
	}
	if *kbps == 16 {
		o.rate = scouterkit.Mic16kbps
	}
	if o.check && o.duration == 0 {
		o.duration = 5 * time.Second
	}

	signal.Ignore(syscall.SIGPIPE) // a closed stdout must not end the program before the microphone is off
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	cfg := scouterkit.DefaultConfig()
	cfg.Adapter, cfg.Log = f.Adapter, f.Log
	cfg.KeepSessions = true // a program on the arm (notifications, a clock) runs on
	if o.arm == scouterkit.Right {
		cfg.Left, cfg.Right = "none", f.Right
	} else {
		cfg.Left, cfg.Right = f.Left, "none"
	}
	if err := scouterkit.Init(cfg); err != nil {
		fail(err)
	}
	_, err := run(ctx, o, os.Stdout)
	scouterkit.Deinit()
	if err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "micsource:", err)
	os.Exit(1)
}

type result struct {
	nodeID          uint32
	sessions        int                 // times the microphone was on
	mic             scouterkit.MicState // the last time
	frames          int                 // the last time
	perSecond       float64             // its frames by the arm's clock
	decoded         level               // its audio after the first 300 ms
	recorded        level               // -check: from the node through PipeWire, after the first sound
	node            sourceStats
	indicatorOn     error // -check: nil when the block was lit while capturing
	indicatorOff    error // -check: nil when it was gone after
	stoppedByItself bool
}

// run makes the node and, until ctx ends or o.duration passes, keeps the
// arm's microphone on while something records from the node (off again o.hold
// after the last recording ends). It returns at once when the microphone stops
// by itself (the link, the panel). The library is initialized.
func run(ctx context.Context, o options, out io.Writer) (result, error) {
	var res result
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if st := o.arm.State(); !st.Microphone {
		return res, fmt.Errorf("the %s arm (g1v0 %s) has no microphone capture (HELLO.board bit 2 is clear); nothing was switched on",
			o.arm, st.Firmware)
	}
	node, err := newSource(o.name, o.description, scouterkit.MicSampleRate, int(o.buffer.Seconds()*scouterkit.MicSampleRate))
	if err != nil {
		return res, err
	}
	defer func() {
		node.Close()
		fmt.Fprintf(out, "node %s removed\n", o.name)
	}()
	res.nodeID = node.Stats().NodeID
	fmt.Fprintf(out, "PipeWire node %s (%q, id %d): 16 kHz mono S16, %v queued; the microphone is on while something records from it\n",
		o.name, o.description, res.nodeID, o.buffer)
	var rec *capture
	if o.check {
		if rec, err = newCapture(o.name, scouterkit.MicSampleRate); err != nil {
			return res, err
		}
		defer rec.Close()
	}

	var frames <-chan scouterkit.MicFrame
	on := false
	var clock armClock
	var second level
	secondFrames, perEvent := 0, 0
	indicator := make(chan error, 1)
	indicatorPending := false
	micOn := func() error {
		if ctx.Err() != nil {
			return nil
		}
		f, err := o.arm.MicStart(o.rate)
		if err != nil {
			return err
		}
		frames, on = f, true
		res.sessions++
		res.frames, res.decoded, clock = 0, level{}, armClock{}
		s := o.arm.MicState()
		perEvent = s.FramesPerEvent
		fmt.Fprintf(out, "something records: microphone on (%s arm, %d-byte LC3 frames, %d per event), the indicator is lit in the panel's outer top corner\n",
			o.arm, s.FrameBytes, s.FramesPerEvent)
		if o.check && res.sessions == 1 {
			indicatorPending = true
			go func() {
				time.Sleep(500 * time.Millisecond)
				indicator <- readIndicator(o.arm, true)
			}()
		}
		return nil
	}
	micOff := func(why string) {
		if !on {
			return
		}
		frames, on = nil, false
		if err := o.arm.MicStop(); err != nil {
			fmt.Fprintf(out, "MIC 0: %v (the arm stops at the disconnection)\n", err)
		}
		res.mic = o.arm.MicState()
		res.perSecond = clock.perSecond()
		fmt.Fprintf(out, "microphone off (%s), indicator off: %d frames, %d gaps, level %.1f dBFS\n", why, res.frames,
			res.mic.Gaps, res.decoded.dbfs())
	}
	defer micOff("exit")

	buf := make([]int16, 4096)
	started := time.Now()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	poll := time.NewTicker(50 * time.Millisecond)
	defer poll.Stop()
	var deadline <-chan time.Time
	if o.duration > 0 {
		deadline = time.After(o.duration)
	}
	idle := time.Now()
	var failure error
loop:
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				res.stoppedByItself = true
				fmt.Fprintf(out, "the microphone stopped by itself: %v\n", o.arm.MicState().Stopped)
				break loop
			}
			node.Write(f.PCM[:])
			res.frames++
			secondFrames++
			clock.add(f, perEvent)
			second.add(f.PCM[:])
			if f.Counter >= 30 && !f.Concealed {
				res.decoded.add(f.PCM[:])
			}
		case <-poll.C:
			n := node.Stats()
			if n.State < 0 {
				failure = errors.New("PipeWire: the node failed")
				break loop
			}
			if n.State == 3 {
				idle = time.Now()
			}
			if !on && n.State == 3 {
				if failure = micOn(); failure != nil {
					break loop
				}
			} else if on && time.Since(idle) > o.hold {
				micOff("nothing records from the node")
			}
			if rec != nil {
				for k := rec.Read(buf); k > 0; k = rec.Read(buf) {
					res.recorded.addAfterSound(buf[:k])
				}
			}
		case err := <-indicator:
			res.indicatorOn, indicatorPending = err, false
		case <-tick.C:
			status(out, time.Since(started), on, o.arm.MicState(), secondFrames, second, node.Stats())
			second, secondFrames = level{}, 0
		case <-deadline:
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	if indicatorPending {
		res.indicatorOn = <-indicator
	}
	res.node = node.Stats() // before the stop: the queue runs dry after it
	micOff("exit")
	if o.check {
		res.indicatorOff = readIndicator(o.arm, false)
		for k := rec.Read(buf); k > 0; k = rec.Read(buf) {
			res.recorded.addAfterSound(buf[:k])
		}
	}
	summary(out, o, res)
	switch {
	case failure != nil:
		return res, failure
	case res.stoppedByItself:
		return res, fmt.Errorf("the microphone stopped: %v", res.mic.Stopped)
	case o.check:
		return res, verdict(o, res)
	}
	return res, nil
}

func status(out io.Writer, t time.Duration, on bool, s scouterkit.MicState, frames int, l level, n sourceStats) {
	pw := "idle: nothing records from it"
	if n.State == 3 {
		pw = fmt.Sprintf("streaming, %d ms queued, rate %+.0f ppm, %d underruns", n.Fill*1000/scouterkit.MicSampleRate,
			(n.Rate-1)*1e6, n.Underruns)
	}
	if !on {
		fmt.Fprintf(out, "%4.0fs  mic off | node %s\n", t.Seconds(), pw)
		return
	}
	fmt.Fprintf(out, "%4.0fs  mic on, indicator on | %3d frames/s, %d gaps, %d concealed | %6.1f dBFS %s | node %s\n",
		t.Seconds(), frames, s.Gaps, s.Missing+s.Bad, l.dbfs(), meter(l.dbfs()), pw)
}

func meter(dbfs float64) string {
	n := int(math.Round((dbfs + 80) / 4))
	n = min(max(n, 0), 20)
	return "[" + strings.Repeat("#", n) + strings.Repeat(".", 20-n) + "]"
}

func summary(out io.Writer, o options, r result) {
	m := r.mic
	if r.sessions == 0 {
		fmt.Fprintln(out, "nothing recorded from the node: the microphone stayed off")
		return
	}
	fmt.Fprintf(out, "frames: %d, %.2f/s by the arm's clock; %d events, %d gaps (%d frames missing), %d undecodable, %d not read in time\n",
		r.frames, r.perSecond, m.Events, m.Gaps, m.Missing, m.Bad, m.Dropped)
	fmt.Fprintf(out, "level: %.1f dBFS RMS, peak %.1f dBFS (%d samples, %d nonzero)\n", r.decoded.dbfs(), r.decoded.peakDBFS(),
		r.decoded.n, r.decoded.nonzero)
	n := r.node
	fmt.Fprintf(out, "node: %d samples played, %d of silence, %d underruns, %d skipped, rate %+.0f ppm (adaptive %v)\n",
		n.Played, n.Silence, n.Underruns, n.Skipped, (n.Rate-1)*1e6, n.Adaptive)
	if o.check {
		fmt.Fprintf(out, "recorded through PipeWire: %d samples, %.1f dBFS RMS, %d nonzero\n", r.recorded.n, r.recorded.dbfs(),
			r.recorded.nonzero)
		fmt.Fprintf(out, "indicator: while on %s; after %s\n", okText(r.indicatorOn, "lit"), okText(r.indicatorOff, "gone"))
	}
}

func okText(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

// verdict of -check: frames at 100/s without gaps, a level above silence, the
// audio through PipeWire, and the indicator.
func verdict(o options, r result) error {
	var bad []string
	want := o.duration.Seconds() * 100
	m := r.mic
	if float64(r.frames) < 0.9*want || math.Abs(r.perSecond-100) > 1 {
		bad = append(bad, fmt.Sprintf("%d frames at %.2f/s (want about %.0f at 100/s)", r.frames, r.perSecond, want))
	}
	if m.Gaps != 0 || m.Bad != 0 || m.Dropped != 0 {
		bad = append(bad, fmt.Sprintf("%d gaps, %d undecodable, %d dropped", m.Gaps, m.Bad, m.Dropped))
	}
	if r.decoded.nonzero == 0 {
		bad = append(bad, "the decoded audio is all zero")
	}
	if float64(r.recorded.n) < 0.8*(o.duration.Seconds()-0.5)*scouterkit.MicSampleRate || r.recorded.nonzero == 0 {
		bad = append(bad, fmt.Sprintf("PipeWire recorded %d samples, %d nonzero", r.recorded.n, r.recorded.nonzero))
	}
	if r.node.Underruns != 0 {
		bad = append(bad, fmt.Sprintf("%d underruns in the node", r.node.Underruns))
	}
	if r.indicatorOn != nil || r.indicatorOff != nil {
		bad = append(bad, "indicator: "+okText(r.indicatorOn, "lit")+"; "+okText(r.indicatorOff, "gone"))
	}
	if len(bad) > 0 {
		return errors.New("check failed: " + strings.Join(bad, "; "))
	}
	return nil
}

// level aggregates samples into sums and counts; it keeps no audio.
type level struct {
	n, nonzero, crossings int
	sum                   float64
	peak                  int
	prev                  int16
}

func (l *level) add(pcm []int16) {
	for _, v := range pcm {
		a := int(v)
		if a < 0 {
			a = -a
		}
		l.peak = max(l.peak, a)
		l.sum += float64(v) * float64(v)
		if v != 0 {
			l.nonzero++
		}
		if l.n > 0 && l.prev < 0 && v >= 0 {
			l.crossings++
		}
		l.prev = v
		l.n++
	}
}

// addAfterSound skips the node's silence before the first sound.
func (l *level) addAfterSound(pcm []int16) {
	if l.n == 0 {
		for len(pcm) > 0 && pcm[0] == 0 {
			pcm = pcm[1:]
		}
	}
	l.add(pcm)
}

func (l *level) dbfs() float64 {
	if l.sum == 0 {
		return -99
	}
	return 10 * math.Log10(l.sum/float64(l.n)/(32768*32768))
}

func (l *level) peakDBFS() float64 {
	if l.peak == 0 {
		return -99
	}
	return 20 * math.Log10(float64(l.peak)/32768)
}

// hz is the frequency of a tone, by its zero crossings.
func (l *level) hz() float64 {
	if l.n == 0 {
		return 0
	}
	return float64(l.crossings) * scouterkit.MicSampleRate / float64(l.n)
}

// armClock measures the frame rate by the arm's clock, from the first frames
// of events (they carry the event's time).
type armClock struct {
	set    bool
	c0, c1 uint32
	t0, t1 uint32
}

func (c *armClock) add(f scouterkit.MicFrame, perEvent int) {
	if perEvent <= 0 || f.Concealed || f.Counter%uint32(perEvent) != 0 {
		return
	}
	if !c.set {
		c.c0, c.t0, c.set = f.Counter, f.DeviceUS, true
	}
	c.c1, c.t1 = f.Counter, f.DeviceUS
}

func (c *armClock) perSecond() float64 {
	dt := float64(c.t1-c.t0) / 1e6
	if dt <= 0 {
		return 0
	}
	return float64(c.c1-c.c0) / dt
}

// readIndicator reads panel rows 4...19 back: with the indicator lit only the
// 16 x 16 block at level 15 in the outer top corner is on (micsource draws
// nothing else); without it the rows are black or the panel is off.
func readIndicator(arm scouterkit.Arm, lit bool) error {
	got, _, err := arm.PanelCRC(4, 16)
	if err != nil {
		if !lit {
			return nil // the panel is off: nothing is shown
		}
		return fmt.Errorf("no read-back (%v)", err)
	}
	if want := indicatorCRC(arm, lit); got != want {
		return fmt.Errorf("rows 4-19 read back %08x, want %08x", got, want)
	}
	return nil
}

func indicatorCRC(arm scouterkit.Arm, lit bool) uint32 {
	row := make([]byte, scouterkit.PanelWidth/2)
	if lit {
		x := 620
		if arm == scouterkit.Left {
			x = 4
		}
		for i := x / 2; i < (x+16)/2; i++ {
			row[i] = 0xFF
		}
	}
	h := crc32.NewIEEE()
	for y := 0; y < 16; y++ {
		h.Write(row)
	}
	return h.Sum32()
}
