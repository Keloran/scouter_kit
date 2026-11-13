//go:build emulator

package scouterkit

/*
#include "scouter_kit_c.h"
*/
import "C"

import (
	"image"
	"time"
	"unsafe"
)

// EmulatedPanel returns what the emulated arm's panel shows (640 x 480, level
// x 17; black while it is off), after the arm ran every command sent before.
// Emulator builds only.
func (a Arm) EmulatedPanel() *image.Gray {
	mu.Lock()
	defer mu.Unlock()
	var got, want C.uint32_t
	C.sk_g1_panel_crc(C.int(a), 0, 1, &got, &want) // waits for the queue; fails while the panel is off
	p := C.sk_g1_emulator_panel(C.int(a))
	if p == nil {
		return nil
	}
	img := image.NewGray(image.Rect(0, 0, PanelWidth, PanelHeight))
	for i, level := range unsafe.Slice((*byte)(unsafe.Pointer(p)), PanelWidth*PanelHeight) {
		img.Pix[i] = level * 17
	}
	return img
}

// SetEmulatedProperty sets a sensor value of the emulated arm (battery,
// case, ...); BATT_PCT follows it with the firmware's hysteresis. Emulator
// builds only.
func (a Arm) SetEmulatedProperty(p Property, value uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_emulator_set_property(C.int(a), C.int(p), C.uint32_t(value))
}

// SetEmulatedMic sets what the emulated arm's microphone hears: a sine of hz
// with its peak at peak x full scale (440 Hz, 0.25 at first; the right arm
// has the microphone). Emulator builds only.
func (a Arm) SetEmulatedMic(hz, peak float64) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_emulator_set_mic(C.int(a), C.double(hz), C.double(peak))
}

// EmulateDisconnect drops the emulated arm's link for d; the library then
// reconnects as it would to real glasses. Emulator builds only.
func (a Arm) EmulateDisconnect(d time.Duration) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_emulator_disconnect(C.int(a), C.int(d.Milliseconds()))
}

// EmulateNewHost makes the emulated arms see a new host: the library forgets
// their sessions and connects again, as a new process with
// Config.KeepSessions = keep would. Emulator builds only.
func EmulateNewHost(keep bool) {
	mu.RLock()
	defer mu.RUnlock()
	k := 0
	if keep {
		k = 1
	}
	C.sk_g1_emulator_new_host(C.int(k))
}
