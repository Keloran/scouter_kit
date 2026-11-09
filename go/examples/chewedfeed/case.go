package main

import (
	"errors"
	"fmt"
	"time"

	"scouterkit"
	"scouterkit/examples/internal/demo"
)

type caseState map[scouterkit.Arm]bool

const caseGuardNotice = 0x43415345

func caseEvent(e scouterkit.Event) (inCase, ok bool) {
	if e.Type == scouterkit.EventNotify && e.A == caseGuardNotice && e.B <= 1 {
		return e.B == 1, true
	}
	if e.Type == scouterkit.EventInput && e.Trigger == int(scouterkit.TriggerCase) {
		// CASE reports lid, charging and case battery bits too; bit 0 is IN_CASE.
		return e.A&1 != 0, true
	}
	return false, false
}

func (s caseState) any() bool {
	for _, inCase := range s {
		if inCase {
			return true
		}
	}
	return false
}

func initialCaseState(arms []scouterkit.Arm) (caseState, error) {
	s := make(caseState)
	for _, arm := range arms {
		values, err := arm.Props(scouterkit.PropInCase)
		if err != nil || len(values) != 1 || values[0] > 1 {
			return nil, fmt.Errorf("%s arm case sensor unavailable", arm)
		}
		s[arm] = values[0] == 1
	}
	return s, nil
}

// Firmware 0.5.60 leaves panel policy to the application. This small program
// turns the arm's panel off on case entry even if the host is disconnected.
// The host separately suppresses all dashboard draws while either arm is cased.
func installCaseGuard(arms []scouterkit.Arm) error {
	base, err := scouterkit.Alloc(1024)
	if err != nil {
		return err
	}
	p := scouterkit.NewProgramAt(base, 16)
	check, done := p.Label(), p.Label()
	c := p.Const
	p.Bind(check)
	p.SysRead(scouterkit.R(1), c(uint32(scouterkit.PropInCase)))
	p.Bne(scouterkit.R(1), c(1), done)
	p.SysWrite(c(uint32(scouterkit.PropPanel)), c(0))
	p.Bind(done)
	p.Notify(c(caseGuardNotice), scouterkit.R(1))
	p.End()
	p.On(scouterkit.TriggerStart, check)
	p.On(scouterkit.TriggerCase, check)
	if p.Here() > base+1024 {
		return errors.New("case guard exceeds its allocation")
	}
	for _, arm := range arms {
		if err := arm.Load(p); err != nil {
			return err
		}
		if err := arm.Start(base); err != nil {
			return err
		}
		if _, err := demo.WaitNotify(arm, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}
