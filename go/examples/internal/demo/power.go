package demo

import (
	"fmt"
	"strings"
	"time"

	"scouterkit"
)

// Current is one arm's battery current over a stretch of time.
type Current struct {
	Arm      scouterkit.Arm
	MA       []int // BATT_MA readings: each the firmware's average of 64 one-second samples
	Charging bool  // CHARGE was not 0 at a reading (the case topped the arm up)
	TempC    float64
	MinC     float64 // the lowest and highest BATT_TEMP read
	MaxC     float64
	Temps    []float64 // BATT_TEMP at each reading
	MV       int
}

// Mean is the average of the readings.
func (c Current) Mean() float64 {
	sum := 0
	for _, v := range c.MA {
		sum += v
	}
	return float64(sum) / float64(max(1, len(c.MA)))
}

func (c Current) String() string {
	if len(c.MA) == 0 {
		return c.Arm.String() + " arm: no reading"
	}
	s := make([]string, len(c.MA))
	for i, v := range c.MA {
		s[i] = fmt.Sprint(v)
	}
	note := ""
	if c.Charging {
		note = " (charging: not a battery load)"
	}
	t := make([]string, len(c.Temps))
	for i, v := range c.Temps {
		t[i] = fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%s arm: %.1f mA (%s), %.1f °C (%.1f…%.1f: %s), %d mV%s", c.Arm, c.Mean(), strings.Join(s, ", "),
		c.TempC, c.MinC, c.MaxC, strings.Join(t, ", "), c.MV, note)
}

// MeasureCurrent reads BATT_MA from every arm 66 s after it starts and then
// every 64 s until d has passed, so each reading covers only the state being
// measured. A closed stop ends it early.
func MeasureCurrent(arms []scouterkit.Arm, d time.Duration, stop <-chan struct{}) []Current {
	start := time.Now()
	out := make([]Current, len(arms))
	for i, a := range arms {
		out[i].Arm = a
	}
	for next := 66 * time.Second; next <= d; next += 64 * time.Second {
		select {
		case <-time.After(time.Until(start.Add(next))):
		case <-stop:
			return out
		}
		for i, a := range arms {
			v, err := a.Props(scouterkit.PropBattMA, scouterkit.PropCharge, scouterkit.PropBattTemp, scouterkit.PropBattMV)
			if err != nil || v[0] == scouterkit.Unknown {
				continue
			}
			out[i].MA = append(out[i].MA, int(int32(v[0])))
			out[i].Charging = out[i].Charging || v[1] != 0
			out[i].TempC = float64(int32(v[2])) / 10
			if len(out[i].MA) == 1 || out[i].TempC < out[i].MinC {
				out[i].MinC = out[i].TempC
			}
			out[i].MaxC = max(out[i].MaxC, out[i].TempC)
			out[i].Temps = append(out[i].Temps, out[i].TempC)
			out[i].MV = int(v[3])
		}
	}
	return out
}

// PrintCurrent measures for d and prints one line per arm under a label.
func PrintCurrent(label string, arms []scouterkit.Arm, d time.Duration, stop <-chan struct{}) []Current {
	fmt.Printf("measuring the battery current (%s) for %v\n", label, d)
	c := MeasureCurrent(arms, d, stop)
	for _, x := range c {
		fmt.Printf("  %s: %s\n", label, x)
	}
	return c
}
