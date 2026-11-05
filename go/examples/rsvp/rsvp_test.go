//go:build emulator

package main

import (
	"math/rand"
	"strings"
	"testing"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

func testText(n int) string {
	vocab := strings.Fields("the a of I reading glasses, arm panel. light quiet; zażółć gęślą jaźń 1984 " +
		"“quoted” Mississippi! antidisestablishmentarianism-and-more (aside) end?")
	rng := rand.New(rand.NewSource(5))
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(vocab[rng.Intn(len(vocab))])
		if rng.Intn(40) == 0 {
			b.WriteString("\n\n")
		} else {
			b.WriteByte(' ')
		}
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

func TestORP(t *testing.T) {
	for w, want := range map[string]int{"a": 0, "I,": 0, "word": 1, "(aside)": 2, "glasses": 2, "Mississippi!": 3,
		"antidisestablishment": 4, "“quoted”": 3} {
		if got := orp(w); got != want {
			t.Errorf("orp(%q) = %d, want %d", w, got, want)
		}
	}
	if pause("end.", false) != 2 || pause("arm,", false) != 1.5 || pause("x", true) != 2.5 || pause("word", false) != 1 {
		t.Error("pauses")
	}
}

// Step mode with and without ORP: the empty band, then every word as the
// host lays it out, on both emulated panels.
func TestSteps(t *testing.T) {
	for _, useORP := range []bool{true, false} {
		t.Run(map[bool]string{true: "orp", false: "centred"}[useORP], func(t *testing.T) {
			cfg, arms := start(t)
			rd, err := load(cfg, arms, testText(200), config{w: 540, wpm: 300, orp: useORP, side: 7, check: 1, ring: 64})
			if err != nil {
				t.Fatal(err)
			}
			if err := rd.steps(80); err != nil {
				t.Fatal(err)
			}
			split := false
			for _, w := range rd.words[:80] {
				split = split || strings.HasSuffix(w.text, "-") && strings.HasPrefix(w.text, "antidis")
			}
			if !split {
				t.Error("the long word was not split")
			}
			for _, arm := range arms {
				m := demo.NewPanel()
				m.Paste(bandImage(rd.font, rd.g, rd.words, 79), rd.pos[arm][0], rd.pos[arm][1])
				if d, x, y := demo.Diff(demo.FromPanel(arm.EmulatedPanel()), m); d != 0 {
					t.Fatalf("%s arm: %d pixels differ, first at (%d, %d)", arm, d, x, y)
				}
			}
		})
	}
}

// Timed at 3000 wpm through a 64-word ring: each arm's words keep the
// text's times (the progress NOTIFYs' clock), and both end on the last word.
func TestTimed(t *testing.T) {
	cfg, arms := start(t)
	rd, err := load(cfg, arms, testText(400), config{w: 540, wpm: 3000, orp: true, side: 7, ring: 64})
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.play(scouterkit.NowUS()+200000, false); err != nil {
		t.Fatal(err)
	}
	first := map[scouterkit.Arm][2]int64{}
	finished := map[scouterkit.Arm]bool{}
	for len(finished) < len(arms) {
		for _, arm := range arms {
			if finished[arm] {
				continue
			}
			e, err := demo.WaitNotify(arm, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			switch e.B >> 24 {
			case waiting:
				t.Fatalf("%s arm: waited for word %d", arm, e.A)
			case ended:
				finished[arm] = true
			case progress:
				at := int64(e.DeviceUS)
				if f, ok := first[arm]; !ok {
					first[arm] = [2]int64{int64(e.A), at}
				} else {
					var want int64
					for i := int(f[0]) - 1; i < int(e.A)-1; i++ {
						want += int64(rd.words[i].micro)
					}
					if d := int64(int32(uint32(at)-uint32(f[1]))) - want; d < -1000 || d > 1000 {
						t.Fatalf("%s arm: words %d...%d took %d µs off their times", arm, f[0], e.A, d)
					}
				}
			}
			rd.refill(arm, int(e.A))
		}
	}
	for _, arm := range arms {
		if err := rd.check(arm, rd.g.total-1); err != nil {
			t.Fatal(err)
		}
	}
	if err := demo.Halted(arms); err != nil {
		t.Fatal(err)
	}
}

// With the host gone and the ring run out, the program ends after the
// give-up time.
func TestGiveUp(t *testing.T) {
	cfg, arms := start(t)
	rd, err := load(cfg, arms, testText(300), config{w: 540, wpm: 3000, orp: true, ring: 64, giveUp: 400 * time.Millisecond})
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
				if e.A != 64 {
					t.Errorf("%s arm: ended at word %d", arm, e.A)
				}
				break
			}
		}
		if !waited || d < 250*time.Millisecond || d > 800*time.Millisecond {
			t.Errorf("%s arm: ended %v after it began to wait", arm, d)
		}
	}
}
