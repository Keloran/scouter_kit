//go:build emulator

package main

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

// testText is n paragraphs of made-up words, some long, some not ASCII.
func testText(n int) string {
	vocab := strings.Fields("the a of reading glasses arm panel light quiet zażółć gęślą jaźń " +
		"Mississippi supercalifragilisticexpialidocious 1984 x, line. word; end! (aside) – “quoted”")
	rng := rand.New(rand.NewSource(3))
	var b strings.Builder
	for p := 0; p < n; p++ {
		for w := 10 + rng.Intn(60); w > 0; w-- {
			b.WriteString(vocab[rng.Intn(len(vocab))])
			b.WriteByte(' ')
		}
		b.WriteString("\n\n")
	}
	return b.String()
}

func start(t *testing.T) (scouterkit.Config, []scouterkit.Arm) {
	t.Helper()
	cfg := scouterkit.DefaultConfig()
	if err := scouterkit.Init(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(scouterkit.Deinit)
	return cfg, demo.Present()
}

// Step mode, whole lines and 5-row steps: after every step both emulated
// panels equal the page the host lays out, while the ring is refilled.
func TestSteps(t *testing.T) {
	for _, step := range []int{0, 5} {
		t.Run(fmt.Sprint("step", step), func(t *testing.T) { testSteps(t, step) })
	}
}

func testSteps(t *testing.T, step int) {
	{
		cfg, arms := start(t)
		rd, err := load(cfg, arms, testText(12), config{w: 540, h: 400, wpm: 200, step: step, check: 1, ring: 30})
		if err != nil {
			t.Fatal(err)
		}
		if err := rd.steps(40); err != nil {
			t.Fatal(err)
		}
		for _, arm := range arms {
			pos, _ := rd.state(arm, wShown)
			got, want := demo.FromPanel(arm.EmulatedPanel()), rd.expected(arm, int(pos))
			if d, x, y := demo.Diff(got, want); d != 0 || int(pos) != 40*rd.g.step {
				t.Fatalf("step %d, %s arm at row %d: %d pixels differ, first at (%d, %d)", step, arm, pos, d, x, y)
			}
		}
		t.Logf("step %d: %d lines, %d in the ring, %d rows per line, %d bytes of code", rd.g.step, rd.g.total, rd.g.ring,
			rd.g.lineH, len(program(rd.l, rd.g, 0, 0, 0).Code()))
	}
}

// Timed to the end with a 24-line ring: the program waits while the host
// holds the lines back, goes on once they come, and ends on the last page.
func TestToTheEnd(t *testing.T) {
	cfg, arms := start(t)
	rd, err := load(cfg, arms, testText(8), config{w: 540, h: 300, wpm: 9000, ring: 24})
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.play(scouterkit.NowUS()+200000, false); err != nil {
		t.Fatal(err)
	}
	waited, finished := map[scouterkit.Arm]bool{}, map[scouterkit.Arm]bool{}
	deadline := time.Now().Add(30 * time.Second)
	for len(finished) < len(arms) && time.Now().Before(deadline) {
		for _, arm := range arms {
			e, err := demo.WaitNotify(arm, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			switch e.B >> 24 {
			case waiting:
				waited[arm] = true
			case ended:
				finished[arm] = true
			}
			if waited[arm] {
				rd.refill(arm, int(e.A))
			}
		}
	}
	if len(waited) != len(arms) || len(finished) != len(arms) {
		t.Fatalf("waited %v, finished %v", waited, finished)
	}
	for _, arm := range arms {
		pos, err := rd.state(arm, wShown)
		if err != nil || int(pos) != rd.g.last() {
			t.Fatalf("%s arm: ended at row %d, %v; the last page is at %d", arm, pos, err, rd.g.last())
		}
		got, want := demo.FromPanel(arm.EmulatedPanel()), rd.expected(arm, int(pos))
		if d, x, y := demo.Diff(got, want); d != 0 {
			t.Fatalf("%s arm: %d pixels differ, first at (%d, %d)", arm, d, x, y)
		}
	}
	if err := demo.Halted(arms); err != nil {
		t.Fatal(err)
	}
}

// With the host gone and the ring run out, the program ends after the
// give-up time instead of waiting with the panel on.
func TestGiveUp(t *testing.T) {
	cfg, arms := start(t)
	rd, err := load(cfg, arms, testText(8), config{w: 540, h: 300, wpm: 9000, ring: 24, giveUp: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.play(scouterkit.NowUS()+100000, false); err != nil {
		t.Fatal(err)
	}
	for _, arm := range arms {
		var waitedAt uint32
		waited := false
		var d time.Duration
		for {
			e, err := demo.WaitNotify(arm, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if e.B>>24 == waiting && !waited {
				waitedAt, waited = e.DeviceUS, true
			}
			d = time.Duration(e.DeviceUS-waitedAt) * time.Microsecond
			if e.B>>24 == ended {
				break
			}
		}
		if !waited || d < 350*time.Millisecond || d > 900*time.Millisecond {
			t.Errorf("%s arm: ended %v after it began to wait", arm, d)
		}
		if pos, _ := rd.state(arm, wShown); int(pos) >= rd.g.last() {
			t.Errorf("%s arm: at row %d, the end is %d", arm, pos, rd.g.last())
		}
	}
}
