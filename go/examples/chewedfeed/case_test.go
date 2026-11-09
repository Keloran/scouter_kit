package main

import (
	"scouterkit"
	"testing"
)

func TestCaseInputsDecodeBitfieldsAndGuardNotifications(t *testing.T) {
	for _, tc := range []struct {
		event      scouterkit.Event
		inCase, ok bool
	}{
		{scouterkit.Event{Type: scouterkit.EventInput, Trigger: int(scouterkit.TriggerCase), A: 0xff01}, true, true},
		{scouterkit.Event{Type: scouterkit.EventInput, Trigger: int(scouterkit.TriggerCase), A: 0x6406}, false, true},
		{scouterkit.Event{Type: scouterkit.EventNotify, A: caseGuardNotice, B: 1}, true, true},
		{scouterkit.Event{Type: scouterkit.EventNotify, A: caseGuardNotice, B: 0}, false, true},
		{scouterkit.Event{Type: scouterkit.EventNotify, A: caseGuardNotice, B: 2}, false, false},
		{scouterkit.Event{Type: scouterkit.EventInput, Trigger: int(scouterkit.TriggerTouch), A: 1}, false, false},
	} {
		inCase, ok := caseEvent(tc.event)
		if inCase != tc.inCase || ok != tc.ok {
			t.Fatalf("case event %+v decoded as %t,%t", tc.event, inCase, ok)
		}
	}
}

func TestCaseVisibilityPreservesManualOffAndNeedsBothArmsOut(t *testing.T) {
	n := newNavigation([]page{{id: "site"}}, []page{{id: "ai"}}, []page{{id: "cpu"}}, false)
	cases := caseState{scouterkit.Left: true, scouterkit.Right: false}
	n.inCase = cases.any()
	if n.visible() || n.touch(3, true) || n.hidden {
		t.Fatal("case should suppress visibility and accidental taps")
	}
	cases[scouterkit.Right] = true
	cases[scouterkit.Left] = false
	n.inCase = cases.any()
	if n.visible() {
		t.Fatal("one arm is still in the case")
	}
	cases[scouterkit.Right] = false
	n.inCase = cases.any()
	if !n.visible() {
		t.Fatal("removing both arms did not restore visibility")
	}
	n.touch(3, true)
	n.inCase = true
	n.inCase = false
	if n.visible() || !n.hidden {
		t.Fatal("case removal overrode manual display-off")
	}
}
