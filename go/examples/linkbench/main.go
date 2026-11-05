// Command linkbench measures the upload rate to the arms: rounds of random
// arena writes, each ended by a NOTIFY, while the VM is idle, while a program
// keeps it busy, or while host code CALLed between the writes runs (the
// pattern of videoplay -stream).
//
//	go run ./examples/linkbench [flags]
package main

import (
	"crypto/rand"
	"flag"
	"fmt"
	"image"
	"os"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type config struct {
	kb, rounds, every int
	mode              string
	busy              time.Duration
	sleep             bool
	long, draws       int
	level             int
	length, on, off   time.Duration
	lit               float64
}

func main() {
	var f demo.Flags
	var c config
	f.Register()
	flag.IntVar(&c.kb, "kb", 64, "KB written per round")
	flag.IntVar(&c.rounds, "rounds", 3, "rounds")
	flag.StringVar(&c.mode, "mode", "idle", `"idle", "program" (a program keeps the VM busy), "host" (host code runs between the writes), "draw" (as host, the code DRAWs), "speed" (the VM's time per instruction and per FILL), "panel" (the panels on with a lit picture and AUTO_BRIGHTNESS, then off, in cycles), "lit" (the panels on with a lit picture) or "readback" (the panels read back at delays after each cold power-on)`)
	flag.DurationVar(&c.busy, "busy", 65*time.Millisecond, "host mode: how long each CALLed code runs")
	flag.IntVar(&c.every, "every", 1700, "host and draw modes: bytes written between CALLs")
	flag.IntVar(&c.draws, "draws", 6, "draw mode: DRAWs of a 640 x 120 band per CALL (~11 ms each on the arm)")
	flag.DurationVar(&c.length, "for", time.Minute, "panel and lit modes: how long")
	flag.Float64Var(&c.lit, "lit", 1, "panel and lit modes: share of the display lit (white)")
	flag.IntVar(&c.level, "level", -1, "panel, lit and readback modes: brightness 0...42 (-1: AUTO_BRIGHTNESS, readback: unchanged)")
	flag.DurationVar(&c.on, "on", 1500*time.Millisecond, "panel mode: how long the panels stay on per cycle")
	flag.DurationVar(&c.off, "off", 500*time.Millisecond, "panel mode: how long they stay off")
	flag.BoolVar(&c.sleep, "sleep", true, "busy code sleeps 1 ms after every 300 jumps (else 100 µs)")
	flag.IntVar(&c.long, "long", 0, "speed mode: only one loop of this many iterations (for a profiler)")
	flag.Parse()
	if c.mode != "idle" && c.mode != "program" && c.mode != "host" && c.mode != "draw" && c.mode != "speed" &&
		c.mode != "panel" && c.mode != "lit" && c.mode != "readback" {
		demo.Usage("usage: linkbench [flags]")
	}
	if err := run(f, c); err != nil {
		fmt.Fprintln(os.Stderr, "linkbench:", err)
		os.Exit(1)
	}
}

// spin is code that runs for busy (a program's START handler: forever), as
// stored code for CALL. It sleeps after every 300 jumps and reads the clock
// just before: a pause between the reading and the sleep (a program's
// run-time cap, preemption) then misses that sleep, and only three misses in
// a row would exceed the 1000 jumps between sleeps. Its variables are words
// after the code, not the registers, which the library's host code uses too.
func spin(base uint32, busy time.Duration, sleep, program bool) (*scouterkit.Program, uint32) {
	p := scouterkit.NewProgramAt(base, 16)
	c := p.Const
	r := func(i int) scouterkit.Word { return scouterkit.Word((base+768)/4 + uint32(i)) }
	now := c(uint32(scouterkit.PropTimeUS))
	nap := c(100)
	if sleep {
		nap = c(1000)
	}
	loop, inner, done, start := p.Label(), p.Label(), p.Label(), p.Label()
	at := p.Here()
	p.Bind(start)
	p.SysRead(r(1), now)
	p.Add(r(2), r(1), c(uint32(busy.Microseconds())))
	p.Bind(loop)
	p.Mov(r(3), c(0))
	p.Bind(inner)
	p.Add(r(3), r(3), c(1))
	p.BltU(r(3), c(300), inner)
	p.SysRead(r(1), now)
	if !program {
		p.BgeU(r(1), r(2), done)
	}
	p.Add(r(4), r(1), nap)
	p.SleepUntil(r(4))
	p.Jmp(loop)
	p.Bind(done)
	p.Ret()
	if program {
		p.On(scouterkit.TriggerStart, start)
	}
	return p, at
}

func run(f demo.Flags, c config) error {
	_, arms, err := demo.Connect(f)
	if err != nil {
		return err
	}
	defer scouterkit.Deinit()
	base, err := scouterkit.Alloc(1024)
	if err != nil {
		return err
	}
	if c.mode == "speed" {
		return speed(arms, base, c.long)
	}
	if c.mode == "panel" || c.mode == "lit" {
		return panels(arms, c)
	}
	if c.mode == "readback" {
		return readback(arms, c)
	}
	n := c.kb * 1024
	area, err := scouterkit.Alloc(n)
	if err != nil {
		return err
	}
	var at uint32
	if c.mode != "idle" {
		var p *scouterkit.Program
		if c.mode == "draw" {
			band, err := scouterkit.Alloc(320 * 120)
			if err != nil {
				return err
			}
			p, at = drawCode(base, band, c.draws)
		} else {
			p, at = spin(base, c.busy, c.sleep, c.mode == "program")
		}
		if err := p.Err(); err != nil {
			return err
		}
		for _, arm := range arms {
			if err := arm.Load(p); err != nil {
				return err
			}
			if c.mode == "program" {
				if err := arm.Start(base); err != nil {
					return err
				}
			}
		}
	}
	every := n
	if c.mode == "host" || c.mode == "draw" {
		every = c.every
	}
	data := make([]byte, n)
	for round := 0; round < c.rounds; round++ {
		rand.Read(data)
		p0 := map[scouterkit.Arm]scouterkit.ArmState{}
		for _, arm := range arms {
			demo.Drain(arm)
			p0[arm] = arm.State()
		}
		t0 := scouterkit.NowUS()
		calls := 0
		for off := 0; off < n; off += every {
			end := min(off+every, n)
			for _, arm := range arms {
				arm.WriteArena(area+uint32(off), data[off:end])
				if c.mode == "host" || c.mode == "draw" {
					arm.Call(at)
				}
			}
			if c.mode == "host" || c.mode == "draw" {
				calls++
			}
		}
		for _, arm := range arms {
			arm.Notify(uint32(round), 0)
		}
		for _, arm := range arms {
			e, err := demo.WaitNotify(arm, 120*time.Second)
			if err != nil {
				return err
			}
			dt := time.Duration(demo.HostUS(arm, e.DeviceUS)-t0) * time.Microsecond
			st := arm.State()
			pk := st.Packets - p0[arm].Packets
			code := ""
			if c.mode == "host" {
				code = fmt.Sprintf(" (%.2f s of code)", (time.Duration(calls) * c.busy).Seconds())
			}
			fmt.Printf("%s %s round %d: %d KB in %.2f s, %.1f KB/s, %d packets (%.0f/s), %d resent, %d calls%s, interval %v\n",
				arm, c.mode, round, c.kb, dt.Seconds(), float64(n)/1024/dt.Seconds(), pk, float64(pk)/dt.Seconds(),
				st.Resent-p0[arm].Resent, calls, code, st.Interval)
		}
	}
	if c.mode == "program" {
		for _, arm := range arms {
			arm.Stop()
		}
	}
	return nil
}

// drawCode is stored code that DRAWs the band (640 x 120, 4 bpp) n times at
// the panel's top.
func drawCode(base, band uint32, n int) (*scouterkit.Program, uint32) {
	p := scouterkit.NewProgramAt(base, 16)
	c := p.Const
	at := p.Here()
	for i := 0; i < n; i++ {
		p.Draw(c(band), c(320), c(0), c(0), c(0), c(0), c(640), c(120))
	}
	p.Ret()
	return p, at
}

// loop is stored code that runs n iterations of 3 instructions (with fill > 0
// also a FILL of fill pixels on one row), sleeping nap µs after every inner,
// then NOTIFYs the time it took in µs and n.
func loop(base, canvas uint32, n, inner, fill, nap int) (*scouterkit.Program, uint32) {
	p := scouterkit.NewProgramAt(base, 16)
	c := p.Const
	r := func(i int) scouterkit.Word { return scouterkit.Word((base+768)/4 + uint32(i)) }
	now := c(uint32(scouterkit.PropTimeUS))
	again, next := p.Label(), p.Label()
	at := p.Here()
	p.SysRead(r(0), now)
	p.Mov(r(1), c(0))
	p.Bind(again)
	p.Mov(r(2), c(0))
	p.Bind(next)
	if fill > 0 {
		p.Fill(c(canvas), c(512), r(2), c(0), c(uint32(fill)), c(1), c(15))
	}
	p.Add(r(1), r(1), c(1))
	p.Add(r(2), r(2), c(1))
	p.BltU(r(2), c(uint32(inner)), next)
	p.SysRead(r(3), now)
	p.Add(r(3), r(3), c(uint32(nap)))
	p.SleepUntil(r(3))
	p.BltU(r(1), c(uint32(n)), again)
	p.SysRead(r(3), now)
	p.Sub(r(3), r(3), r(0))
	p.Notify(r(3), r(1))
	p.Ret()
	return p, at
}

func speed(arms []scouterkit.Arm, base uint32, long int) error {
	canvas, err := scouterkit.Alloc(512)
	if err != nil {
		return err
	}
	tests := []struct{ n, inner, fill int }{{900, 900, 0}, {16000, 800, 0}, {16000, 400, 0}, {16000, 200, 0}, {900, 900, 8}, {16000, 800, 8}}
	if long > 0 {
		tests = tests[:0]
		tests = append(tests, struct{ n, inner, fill int }{long, 800, 0})
	}
	for _, t := range tests {
		n, fill := t.n, t.fill
		nap := 100
		if long > 0 {
			nap = 20000
		}
		p, at := loop(base, canvas, n, t.inner, fill, nap)
		if err := p.Err(); err != nil {
			return err
		}
		for _, arm := range arms {
			demo.Drain(arm)
			if err := arm.Load(p); err != nil {
				return err
			}
			arm.Call(at)
			e, err := demo.WaitNotify(arm, 60*time.Second)
			if err != nil {
				return err
			}
			us := float64(e.A) - float64(n/t.inner*nap)
			instr := 3.0
			if fill > 0 {
				instr = 4
			}
			fmt.Printf("%s speed: %d iterations of %.0f instructions, a sleep after %d (FILL of %d pixels: %v) in %.1f ms: %.2f µs per iteration, %.2f µs per instruction\n",
				arm, e.B, instr, t.inner, fill, fill > 0, us/1000, us/float64(e.B), us/float64(e.B)/instr)
		}
	}
	return nil
}

// panels lights a share of the display white and, in panel mode, switches
// the panels off and on again in cycles; with AUTO_BRIGHTNESS (-level -1) the
// light sensor, on the panels' rails, converts while they are on. Each cycle (or
// every second) prints each arm's battery voltage and current and the light
// reading, and any arm that rebooted.
func panels(arms []scouterkit.Arm, c config) error {
	w, h := scouterkit.Size()
	b, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return err
	}
	defer b.Unload()
	b.Fill(image.Rect(0, 0, w, h), 0)
	b.Fill(image.Rect(0, 0, w, int(float64(h)*c.lit+0.5)), 255)
	epochs := map[scouterkit.Arm]uint32{}
	for _, arm := range arms {
		epochs[arm] = arm.State().EpochChanges
		light(arm, c.level)
	}
	t0 := time.Now()
	for cycle := 0; time.Since(t0) < c.length; cycle++ {
		scouterkit.Draw(b, 0, 0)
		scouterkit.Flush()
		if c.mode == "panel" {
			time.Sleep(c.on)
		} else {
			time.Sleep(time.Second)
		}
		line := fmt.Sprintf("%s %7.1f s cycle %d", time.Now().Format("15:04:05"), time.Since(t0).Seconds(), cycle)
		for _, arm := range arms {
			v, err := arm.Props(scouterkit.PropBattMV, scouterkit.PropBattMA, scouterkit.PropLight, scouterkit.PropBrightness,
				scouterkit.PropPanel, scouterkit.PropBattPct, scouterkit.PropBrightnessCap, scouterkit.PropBattTemp)
			st := arm.State()
			if err != nil {
				line += fmt.Sprintf("; %s: %v", arm, err)
			} else {
				line += fmt.Sprintf("; %s %d mV %d mA light %d level %d panel %d %d%% cap %d %.1f C", arm, v[0], int32(v[1]),
					v[2], v[3], v[4], v[5], v[6], float64(int32(v[7]))/10)
			}
			if st.EpochChanges != epochs[arm] {
				line += " REBOOTED"
				epochs[arm] = st.EpochChanges
				light(arm, c.level)
			}
		}
		fmt.Println(line)
		if c.mode == "panel" {
			for _, arm := range arms {
				arm.SetPanel(false)
			}
			// the library shows black too (the eyes stay off), so the next picture is a change
			scouterkit.Clear()
			scouterkit.Flush()
			time.Sleep(c.off)
		}
	}
	for _, arm := range arms {
		arm.SysWrite(scouterkit.PropAutoBrightness, 0)
		arm.SetPanel(false)
	}
	return nil
}

func light(arm scouterkit.Arm, level int) {
	if level < 0 {
		arm.SysWrite(scouterkit.PropAutoBrightness, 1)
	} else {
		arm.SetBrightness(level)
	}
}

// readback switches the panels off for -off, draws 1-pixel stripes on rows
// 40...199 (the panels power on cold) and reads those rows back at delays
// after the draw ran, -rounds times.
func readback(arms []scouterkit.Arm, c config) error {
	w, h := scouterkit.Size()
	b, err := scouterkit.CreateBitmap(w, h)
	if err != nil {
		return err
	}
	defer b.Unload()
	b.Fill(image.Rect(0, 0, w, h), 0)
	for x := 0; x < w; x += 2 {
		b.Fill(image.Rect(x, 40, x+1, 200), 255)
	}
	if c.level >= 0 {
		for _, arm := range arms {
			arm.SetBrightness(c.level)
		}
	}
	delays := []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
		time.Second, 2 * time.Second}
	for round := 0; round < c.rounds; round++ {
		for _, arm := range arms {
			arm.SetPanel(false)
		}
		scouterkit.Clear()
		scouterkit.Flush()
		time.Sleep(c.off)
		scouterkit.Draw(b, 0, 0)
		scouterkit.Flush()
		t0 := time.Now()
		line := fmt.Sprintf("%s round %d:", time.Now().Format("15:04:05"), round)
		for _, d := range delays {
			time.Sleep(time.Until(t0.Add(d)))
			for _, arm := range arms {
				got, want, err := arm.PanelCRC(40, 160)
				switch {
				case err != nil:
					line += fmt.Sprintf(" %s@%v FAIL", arm, d)
				case got != want:
					line += fmt.Sprintf(" %s@%v DIFF", arm, d)
				default:
					line += fmt.Sprintf(" %s@%v ok", arm, d)
				}
			}
		}
		fmt.Println(line)
	}
	for _, arm := range arms {
		arm.SetPanel(false)
	}
	return nil
}
