package main

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync/atomic"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// Codec mode (-levels 2, the default): the PC encodes each frame as a VIDEO
// record (VM.md §3.7) against the last frame it sent, and each arm decodes it
// into a 1-bpp bitmap and draws the rows that changed at the frame's time.
// Per frame and arm: one WRITE of the due time, the frame index, the size and
// the record, then a CALL of stored code that sleeps until the due time, runs
// VIDEO and NOTIFYs (frame, µs of VIDEO). A frame the link cannot deliver in
// time is skipped before it is encoded, so the arms never miss a record.

const (
	cDue = iota
	cFrame
	cSize
	cHeader
)

// recordMax: the largest record sent; a frame whose record would be larger is skipped.
const recordMax = 32768

type codecLayout struct {
	base, code, bitmap, state, buf uint32
	stride                         int
}

func newCodecLayout(base uint32, w, h int) codecLayout {
	l := codecLayout{base: base, stride: ((w+7)/8 + 3) &^ 3}
	l.code = base
	l.bitmap = base + 512
	l.state = l.bitmap + uint32(l.stride*h)
	l.buf = (l.state + scouterkit.VideoState + 3) &^ 3
	return l
}

func (l codecLayout) size() int { return int(l.buf-l.base) + 4*cHeader + recordMax }

func codecCode(l codecLayout, w, h, x, y int) (*scouterkit.Program, uint32) {
	p := scouterkit.NewProgramAt(l.code, 16)
	c := p.Const
	r := scouterkit.R
	word := func(i int) scouterkit.Word { return scouterkit.Word(l.buf/4 + uint32(i)) }
	now := c(uint32(scouterkit.PropTimeUS))
	at := p.Here()
	p.SleepUntil(word(cDue))
	p.SysRead(r(1), now)
	p.Video(c(l.bitmap), c(uint32(l.stride)), c(uint32(w)), c(uint32(h)), c(uint32(int32(x))), c(uint32(int32(y))),
		c(15), c(l.buf+4*cHeader), word(cSize), c(l.state))
	p.SysRead(r(2), now)
	p.Sub(r(2), r(2), r(1))
	p.Notify(word(cFrame), r(2))
	p.Ret()
	return p, at
}

func codecPlay(cfg scouterkit.Config, arms []scouterkit.Arm, c config, w, h, x, y int, r io.Reader) error {
	probe := newCodecLayout(0, w, h)
	base, err := scouterkit.Alloc(probe.size())
	if err != nil {
		return err
	}
	defer scouterkit.Free(base)
	l := newCodecLayout(base, w, h)
	at := map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		ax, ay := demo.PanelAt(cfg, arm, x, y)
		code, call := codecCode(l, w, h, ax, ay)
		if err := code.Err(); err != nil {
			return err
		}
		arm.WriteArena(l.bitmap, make([]byte, l.stride*h))
		if err := arm.Load(code); err != nil {
			return err
		}
		at[arm] = call
	}

	enc := scouterkit.NewVideoEncoder(w, h)
	defer enc.Close()
	period := time.Duration(float64(time.Second) / c.fps)
	ahead := int(c.sendLead/period) + 2
	frame, pix, shown := make([]byte, w*h), make([]byte, w*h), make([]byte, w*h)
	notified, maxVideo, sumVideo := map[scouterkit.Arm]int{}, map[scouterkit.Arm]time.Duration{}, map[scouterkit.Arm]time.Duration{}
	maxLate, devDue := map[scouterkit.Arm]time.Duration{}, map[scouterkit.Arm]map[uint32]uint32{}
	packets0 := map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		devDue[arm] = map[uint32]uint32{}
		packets0[arm] = arm.State().Packets
	}
	var audio *exec.Cmd
	t0 := time.Now().Add(c.sendLead + 300*time.Millisecond)
	sent, skipped, identical, bytes, last := 0, 0, 0, 0, uint32(0)
	stop := demo.Interrupted()

	events := func() error {
		for _, arm := range arms {
			for {
				e, ok := arm.NextEvent(0)
				if !ok {
					break
				}
				switch e.Type {
				case scouterkit.EventNotify:
					notified[arm]++
					d := time.Duration(e.B) * time.Microsecond
					maxVideo[arm] = max(maxVideo[arm], d)
					sumVideo[arm] += d
					if due, ok := devDue[arm][e.A]; ok {
						maxLate[arm] = max(maxLate[arm], time.Duration(int32(e.DeviceUS-due))*time.Microsecond-d)
						delete(devDue[arm], e.A)
					}
				case scouterkit.EventHalt, scouterkit.EventError:
					if e.Code == 2 {
						return fmt.Errorf("%s arm: its firmware has no VIDEO; play with -upload", arm)
					}
					return fmt.Errorf("%s arm: event %d, code %d at %#x", arm, e.Type, e.Code, e.Address)
				}
			}
		}
		return nil
	}
	// An arm that rebooted (a new epoch) lost the frames in flight: the library restores its bitmap,
	// state and panel from the mirror, and their NOTIFYs never come.
	epochs := map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		epochs[arm] = arm.State().EpochChanges
	}
	busy := func() bool {
		for _, arm := range arms {
			st := arm.State()
			if st.EpochChanges != epochs[arm] {
				epochs[arm] = st.EpochChanges
				notified[arm] = sent
				devDue[arm] = map[uint32]uint32{}
				fmt.Printf("%s arm: rebooted; going on from its restored state\n", arm)
			}
			if st.Queued >= 24 || sent-notified[arm] >= ahead {
				return true
			}
		}
		return false
	}

	var litFrame, litShare atomic.Uint32
	if c.telemetry > 0 {
		done := make(chan struct{})
		defer close(done)
		go telemetry(arms, c.telemetry, &litFrame, &litShare, done)
	}
	interrupted := false
	for k := uint32(0); ; k++ {
		if _, err := io.ReadFull(r, frame); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return err
		}
		select {
		case <-stop:
			interrupted = true
		default:
		}
		if interrupted {
			break
		}
		if err := events(); err != nil {
			return err
		}
		due := t0.Add(time.Duration(k) * period)
		sleepUntil(due.Add(-c.sendLead))
		for waited := time.Now(); busy(); {
			if time.Since(waited) > 10*time.Second {
				return errors.New("an arm has not taken a frame for 10 s")
			}
			time.Sleep(2 * time.Millisecond)
			if err := events(); err != nil {
				return err
			}
		}
		if c.audio && audio == nil && time.Now().After(t0.Add(c.audioDelay)) {
			audio = startAudio(c)
		}
		if k > 0 && time.Until(due) < c.sendLead/2 {
			skipped++
			continue
		}
		same, lit := k > 0, 0
		for i, v := range frame {
			pix[i] = 0
			if int(v) >= c.threshold {
				pix[i] = 1
				lit++
			}
			same = same && pix[i] == shown[i]
		}
		litFrame.Store(k)
		litShare.Store(uint32(lit * 1000 / len(pix)))
		if same {
			identical++
			continue
		}
		rec := enc.Encode(pix, k == 0, recordMax)
		if rec == nil {
			skipped++
			continue
		}
		copy(shown, pix)
		for _, arm := range arms {
			d := arm.DeviceTime(time.Until(due))
			devDue[arm][k] = d
			arm.WriteArena(l.buf, append(demo.Words(d, k, uint32(len(rec))), rec...))
			arm.Call(at[arm])
		}
		sent, bytes, last = sent+1, bytes+len(rec), k
	}
	deadline := time.Now().Add(10 * time.Second)
	for !interrupted && time.Now().Before(deadline) {
		if err := events(); err != nil {
			return err
		}
		all := true
		for _, arm := range arms {
			all = all && notified[arm] == sent
		}
		if all {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	elapsed := time.Since(t0)
	fmt.Printf("%d frames sent, %d skipped, %d unchanged, %.0f B per frame, in %v (%.1f frames/s, last frame %d)\n",
		sent, skipped, identical, float64(bytes)/float64(max(1, sent)), elapsed.Round(time.Millisecond),
		float64(sent)/elapsed.Seconds(), last)
	for _, arm := range arms {
		n := time.Duration(max(1, notified[arm]))
		fmt.Printf("%s arm: VIDEO %v on average, at most %v; at most %v late; %.0f packets/s\n", arm,
			(sumVideo[arm] / n).Round(100*time.Microsecond), maxVideo[arm].Round(100*time.Microsecond),
			maxLate[arm].Round(time.Millisecond), float64(arm.State().Packets-packets0[arm])/elapsed.Seconds())
	}
	if audio != nil {
		if interrupted {
			audio.Process.Kill()
		}
		audio.Wait()
	}

	failed := false
	if c.check && !interrupted {
		img := make([]byte, w*h)
		for i, v := range shown {
			img[i] = 15 * v
		}
		for _, arm := range arms {
			want := demo.NewPanel()
			ax, ay := demo.PanelAt(cfg, arm, x, y)
			want.Paste(&demo.Image{W: w, H: h, Pix: img}, ax, ay)
			if err := demo.CheckPanel(arm, want, 0, scouterkit.PanelHeight); err != nil {
				fmt.Println(err)
				failed = true
			} else {
				fmt.Printf("%s panel: as expected\n", arm)
			}
		}
	}
	for _, arm := range arms {
		arm.SetPanel(false)
	}
	if failed {
		return errors.New("a panel did not read back as expected")
	}
	return nil
}

// telemetry prints, every interval until done, the frame being sent and its
// lit share, and each arm's battery voltage and current, panel state and
// reboots.
func telemetry(arms []scouterkit.Arm, every time.Duration, frame, share *atomic.Uint32, done chan struct{}) {
	epochs := map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		epochs[arm] = arm.State().EpochChanges
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
		}
		line := fmt.Sprintf("telemetry %s frame %d lit %.1f%%", time.Now().Format("15:04:05.0"), frame.Load(),
			float64(share.Load())/10)
		for _, arm := range arms {
			v, err := arm.Props(scouterkit.PropBattMV, scouterkit.PropBattMA, scouterkit.PropPanel, scouterkit.PropBattPct,
				scouterkit.PropBrightness, scouterkit.PropBrightnessMax, scouterkit.PropBrightnessCap)
			if err != nil {
				line += fmt.Sprintf("; %s: %v", arm, err)
			} else {
				line += fmt.Sprintf("; %s %d mV %d mA panel %d %d%% level %d max %d cap %d", arm, v[0], int32(v[1]), v[2],
					v[3], v[4], v[5], v[6])
			}
			if st := arm.State(); st.EpochChanges != epochs[arm] {
				epochs[arm] = st.EpochChanges
				line += " REBOOTED"
			}
		}
		fmt.Println(line)
	}
}
