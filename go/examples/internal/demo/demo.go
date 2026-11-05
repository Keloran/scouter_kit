// Package demo has what the VM demos share: connection flags, the eyes'
// geometry, expected panel images and their read-back, events, clocks and
// battery current.
package demo

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"scouterkit"
)

// Flags are the connection flags of every demo.
type Flags struct {
	Left, Right, Adapter string
	Log                  bool
}

func (f *Flags) Register() {
	flag.StringVar(&f.Left, "left", "", `left arm address (empty: the bonded glasses, "none": no left arm)`)
	flag.StringVar(&f.Right, "right", "", `right arm address (empty: the bonded glasses, "none": no right arm)`)
	flag.StringVar(&f.Adapter, "adapter", "", `BlueZ adapter, "hci1" or its address (empty: the first powered one)`)
	flag.BoolVar(&f.Log, "log", false, "the library logs to stderr")
}

// Connect initializes the library and returns its configuration and the
// arms in use.
func Connect(f Flags) (scouterkit.Config, []scouterkit.Arm, error) {
	cfg := scouterkit.DefaultConfig()
	cfg.Left, cfg.Right, cfg.Adapter = f.Left, f.Right, f.Adapter
	cfg.Log = f.Log
	if err := scouterkit.Init(cfg); err != nil {
		return cfg, nil, err
	}
	return cfg, Present(), nil
}

// Present returns the arms the library talks to.
func Present() []scouterkit.Arm {
	var arms []scouterkit.Arm
	for _, a := range []scouterkit.Arm{scouterkit.Left, scouterkit.Right} {
		if a.State().Present {
			arms = append(arms, a)
		}
	}
	return arms
}

// PanelAt is where an eye shows virtual display pixel (x, y).
func PanelAt(cfg scouterkit.Config, arm scouterkit.Arm, x, y int) (int, int) {
	if arm == scouterkit.Left {
		return x - max(0, -cfg.DX), y + max(0, -cfg.DY)
	}
	return x - max(0, cfg.DX), y + max(0, cfg.DY)
}

// Usage sets the usage line printed before the flags.
func Usage(line string) {
	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "usage:", line)
		flag.PrintDefaults()
	}
}

// Interrupted is closed on Ctrl-C.
func Interrupted() <-chan struct{} {
	done := make(chan struct{})
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		close(done)
	}()
	return done
}

// CheckPanel compares the arm's panel rows y0...y0+rows-1 with want, a
// 640 x 480 image.
func CheckPanel(arm scouterkit.Arm, want *Image, y0, rows int) error {
	got, _, err := arm.PanelCRC(y0, rows)
	if err != nil { // the arm could not verify its read (a row read three ways) or did not answer: once more
		time.Sleep(200 * time.Millisecond)
		got, _, err = arm.PanelCRC(y0, rows)
	}
	if err != nil {
		return fmt.Errorf("%s arm: panel read-back: %w", arm, err)
	}
	if w := crc32.ChecksumIEEE(want.Rows(y0, rows)); got != w {
		return fmt.Errorf("%s arm: panel rows %d...%d read back %08x, expected %08x (rows differing: %s)", arm, y0,
			y0+rows-1, got, w, differing(arm, want, y0, rows))
	}
	return nil
}

// differing lists the rows whose read-back differs from want, as ranges.
func differing(arm scouterkit.Arm, want *Image, y0, rows int) string {
	var out []string
	start := -1
	for y := y0; y <= y0+rows; y++ {
		bad := false
		if y < y0+rows {
			got, _, err := arm.PanelCRC(y, 1)
			bad = err != nil || got != crc32.ChecksumIEEE(want.Rows(y, 1))
		}
		if bad && start < 0 {
			start = y
		} else if !bad && start >= 0 {
			out = append(out, fmt.Sprintf("%d-%d", start, y-1))
			start = -1
		}
	}
	return strings.Join(out, ",")
}

// WaitNotify returns the arm's next NOTIFY (of its program, or of host code
// that called stored code). A HALT is an error.
func WaitNotify(arm scouterkit.Arm, timeout time.Duration) (scouterkit.Event, error) {
	deadline := time.Now().Add(timeout)
	for {
		e, ok := arm.NextEvent(time.Until(deadline))
		switch {
		case !ok:
			return e, fmt.Errorf("%s arm: no NOTIFY within %v", arm, timeout)
		case e.Type == scouterkit.EventHalt:
			return e, fmt.Errorf("%s arm: the program halted: code %d, op 0x%02x at 0x%05x, detail 0x%x", arm,
				e.Code, e.Op, e.Address, e.Detail)
		case e.Type == scouterkit.EventError:
			return e, fmt.Errorf("%s arm: host code refused: code %d, op 0x%02x, detail 0x%x", arm, e.Code, e.Op, e.Detail)
		case e.Type == scouterkit.EventReset:
			return e, errors.New(arm.String() + " arm: it rebooted (session reset)")
		case e.Type == scouterkit.EventNotify:
			return e, nil
		}
	}
}

// Drain drops the arm's queued events.
func Drain(arm scouterkit.Arm) {
	for {
		if _, ok := arm.NextEvent(0); !ok {
			return
		}
	}
}

// Lead is how far ahead to schedule something both eyes show, from now:
// two connection intervals, the clock error and 15 ms (VM.md §7).
func Lead(arms []scouterkit.Arm) time.Duration {
	d := 30 * time.Millisecond
	for _, arm := range arms {
		s := arm.State()
		d = max(d, 2*s.Interval+s.ClockError+15*time.Millisecond)
	}
	return d
}

// Before24 is the device time whose low 24 bits are t24, at or before the
// event's time (within 16 s).
func Before24(e scouterkit.Event, t24 uint32) uint32 {
	return e.DeviceUS - (e.DeviceUS-t24)&0xFFFFFF
}

// HostUS converts a device time of the arm (within half an hour of now) to
// the host clock of scouterkit.NowUS.
func HostUS(arm scouterkit.Arm, device uint32) int64 {
	now := scouterkit.NowUS()
	return now + int64(int32(device-arm.DeviceTime(0)))
}

// Degrees formats an angle property (PITCH, ROLL).
func Degrees(v uint32) string {
	if v == scouterkit.Unknown {
		return "unknown"
	}
	return fmt.Sprintf("%d°", int32(v))
}

// Halted reports a program that is not running on one of the arms.
func Halted(arms []scouterkit.Arm) error {
	for _, arm := range arms {
		s, err := arm.VMStatus()
		if err != nil {
			return fmt.Errorf("%s arm: VM_STATUS: %w", arm, err)
		}
		if s.State != 1 {
			return fmt.Errorf("%s arm: program state %d (halt code %d at 0x%05x)", arm, s.State, s.HaltCode, s.Next)
		}
	}
	return nil
}

// Status prints each arm's program counters.
func Status(arms []scouterkit.Arm) {
	for _, arm := range arms {
		s, err := arm.VMStatus()
		if err != nil {
			fmt.Printf("%s arm: VM_STATUS: %v\n", arm, err)
			continue
		}
		fmt.Printf("%s arm: state %d, %d tasks, %d coalesced, %d cap pauses, busy %d ms of the last 100 ms\n",
			arm, s.State, s.Tasks, s.Coalesced, s.CapPauses, s.BusyMS)
	}
}

// Stop stops the arms' programs and switches the panels off.
func Stop(arms []scouterkit.Arm) {
	for _, arm := range arms {
		if err := arm.Stop(); err != nil {
			fmt.Fprintf(os.Stderr, "%s arm: VM_STOP: %v\n", arm, err)
		}
		arm.SetPanel(false)
	}
}

// Skew collects how far apart the two arms reported the same step (on the
// host clock, within the clock estimates' error).
type Skew struct {
	seen map[uint32]map[scouterkit.Arm]int64
	ms   []float64
}

// Add notes the arm's event for step k; it returns the difference in ms once
// both arms have reported k.
func (s *Skew) Add(arm scouterkit.Arm, k uint32, e scouterkit.Event) (float64, bool) {
	return s.At(arm, k, e.DeviceUS)
}

// At is Add with the device time of the step itself.
func (s *Skew) At(arm scouterkit.Arm, k, device uint32) (float64, bool) {
	if s.seen == nil {
		s.seen = map[uint32]map[scouterkit.Arm]int64{}
	}
	if s.seen[k] == nil {
		s.seen[k] = map[scouterkit.Arm]int64{}
	}
	s.seen[k][arm] = HostUS(arm, device)
	if len(s.seen[k]) < 2 {
		return 0, false
	}
	d := float64(s.seen[k][scouterkit.Left]-s.seen[k][scouterkit.Right]) / 1000
	delete(s.seen, k)
	s.ms = append(s.ms, d)
	return d, true
}

func (s *Skew) String() string {
	if len(s.ms) == 0 {
		return "no step seen on both arms"
	}
	a := make([]float64, len(s.ms))
	for i, v := range s.ms {
		a[i] = math.Abs(v)
	}
	sort.Float64s(a)
	return fmt.Sprintf("eyes apart: median %.1f ms, max %.1f ms over %d steps", a[len(a)/2], a[len(a)-1], len(a))
}

// Away drops the arms' links for d, waits until they are back with their
// sessions, and checks that their programs still run.
func Away(arms []scouterkit.Arm, d time.Duration) error {
	fmt.Printf("dropping the links for %v\n", d)
	for _, arm := range arms {
		arm.Disconnect(d)
	}
	time.Sleep(d)
	deadline := time.Now().Add(2 * time.Minute)
	for _, arm := range arms {
		for !arm.State().Connected && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if s := arm.State(); !s.Connected || s.EpochChanges != 0 {
			return fmt.Errorf("%s arm: not back with its session (connected %v, reboots %d)", arm, s.Connected, s.EpochChanges)
		}
	}
	return Halted(arms)
}

// Words is the values as little-endian arena words.
func Words(v ...uint32) []byte {
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], x)
	}
	return b
}
