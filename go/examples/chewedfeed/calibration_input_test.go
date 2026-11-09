package main

import (
	"reflect"
	"testing"
)

func TestCalibrationKeysAndVisualDirection(t *testing.T) {
	var p calibrationKeyParser
	var got []calibrationKey
	// Includes CSI, SS3, modified arrows, ignored keys, and an unrelated CSI final.
	for _, b := range []byte("\x1b[A\x1b[B\x1bOC\x1bOD\x1b[1;5CxsS\x1b[5~\x1b[6~\x1b[9~q") {
		if key := p.feed(b); key != 0 {
			got = append(got, key)
		}
	}
	want := []calibrationKey{calibrationUp, calibrationDown, calibrationRight, calibrationLeft,
		calibrationRight, calibrationSave, calibrationSave, calibrationLarger, calibrationSmaller, calibrationQuit}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("keys: %v, want %v", got, want)
	}
	for _, tc := range []struct {
		key  calibrationKey
		x, y int
	}{{calibrationLeft, 2, -6}, {calibrationRight, 0, -6}, {calibrationUp, 1, -7}, {calibrationDown, 1, -5}} {
		o := nudgeCalibration(options{dx: 1, dy: -6, fontScale: 1.5}, tc.key)
		if o.dx != tc.x || o.dy != tc.y || o.fontScale != 1.5 {
			t.Fatalf("key %v: %+v", tc.key, o)
		}
	}
}

func TestCalibrationFontStepsAndLimits(t *testing.T) {
	o := options{dx: -15, dy: -8, fontScale: 1.5}
	for _, key := range []calibrationKey{calibrationLarger, calibrationLarger, calibrationSmaller} {
		o = nudgeCalibration(o, key)
	}
	if o.fontScale != 1.6 || o.dx != -15 || o.dy != -8 {
		t.Fatalf("font steps changed alignment or drifted: %+v", o)
	}
	for i := 0; i < 100; i++ {
		o = nudgeCalibration(o, calibrationLarger)
	}
	if o.fontScale != 3 {
		t.Fatalf("upper font limit: %g", o.fontScale)
	}
	for i := 0; i < 100; i++ {
		o = nudgeCalibration(o, calibrationSmaller)
	}
	if o.fontScale != 0.1 {
		t.Fatalf("lower font limit: %g", o.fontScale)
	}
	o = nudgeCalibration(o, calibrationLarger)
	if o.fontScale != 0.2 {
		t.Fatalf("increasing from the minimum: %g", o.fontScale)
	}
}
