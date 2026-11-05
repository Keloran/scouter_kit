package scouterkit

/*
#include <stdlib.h>
#include "scouter_kit_c.h"
*/
import "C"

import (
	"fmt"
	"time"
	"unsafe"
)

// The size of each arm's panel.
const (
	PanelWidth  = 640
	PanelHeight = 480
)

// Arm is one eye of the glasses, for the few things that are per arm.
type Arm int

const (
	Left Arm = iota
	Right
)

func (a Arm) String() string {
	if a == Left {
		return "left"
	}
	return "right"
}

type ArmState struct {
	Present      bool // configured (not "none")
	Connected    bool // link up and session ready
	Firmware     string
	Address      string
	Interval     time.Duration // connection interval
	ClockError   time.Duration // bound of the clock estimate
	Idle         bool          // the idle link profile is asked for
	Queued       int           // commands not sent yet
	Packets      uint32
	Resent       uint32
	Reconnects   uint32
	Epoch        uint32
	EpochChanges uint32 // the arm lost its memory (rebooted) and was restored
	Errors       uint32 // commands refused by the library or the arm
	LastError    string
	ShownUpdate  uint64 // the last Flush (1, 2, ...) this eye showed
	PanelOn      bool   // as the library expects it
	SafeMode     bool
	Encrypted    bool
	InCase       bool
	Microphone   bool   // MIC captures and streams on this firmware
	Triggers     uint16 // that this firmware can raise (bit i = trigger i)
	EventsLost   uint32
}

func (a Arm) State() ArmState {
	mu.RLock()
	defer mu.RUnlock()
	var s C.sk_g1_state
	C.sk_g1_get_state(C.int(a), &s)
	state := ArmState{
		Present:      s.present != 0,
		Connected:    s.connected != 0,
		Address:      C.GoString(&s.address[0]),
		Interval:     time.Duration(s.interval_us) * time.Microsecond,
		ClockError:   time.Duration(s.clock_error_us) * time.Microsecond,
		Idle:         s.idle != 0,
		Queued:       int(s.queued),
		Packets:      uint32(s.packets),
		Resent:       uint32(s.resent),
		Reconnects:   uint32(s.reconnects),
		Epoch:        uint32(s.epoch),
		EpochChanges: uint32(s.epoch_changes),
		Errors:       uint32(s.errors),
		LastError:    C.GoString(&s.last_error[0]),
		ShownUpdate:  uint64(s.shown_update),
		PanelOn:      s.panel_on != 0,
		SafeMode:     s.safe_mode != 0,
		Encrypted:    s.encrypted != 0,
		InCase:       s.in_case != 0,
		Microphone:   s.microphone != 0,
		Triggers:     uint16(s.triggers),
		EventsLost:   uint32(s.events_lost),
	}
	if state.Connected {
		state.Firmware = fmt.Sprintf("%d.%d.%d", s.firmware_major, s.firmware_minor, s.firmware_patch)
	}
	return state
}

// SetBrightness sets the panel's brightness, 0...42.
func (a Arm) SetBrightness(level int) error {
	if level < 0 || level > 42 {
		return fmt.Errorf("scouterkit: brightness %d is not in 0...42", level)
	}
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_set_brightness(C.int(a), C.int(level)) == 0 {
		return ErrNoReply
	}
	return nil
}

// SetPanel switches the panel off (its image is lost) or on (all black).
func (a Arm) SetPanel(on bool) {
	mu.RLock()
	defer mu.RUnlock()
	v := 0
	if on {
		v = 1
	}
	C.sk_g1_set_panel(C.int(a), C.int(v))
}

type Battery struct {
	Millivolts int // -1: unknown
	Percent    int // -1: unknown
	Charging   bool
}

func (a Arm) Battery() (Battery, error) {
	mu.RLock()
	defer mu.RUnlock()
	var mv, percent, charging C.int
	if C.sk_g1_battery(C.int(a), &mv, &percent, &charging) == 0 {
		return Battery{}, ErrNoReply
	}
	return Battery{Millivolts: int(mv), Percent: int(percent), Charging: charging != 0}, nil
}

// PanelCRC reads panel rows y0...y0+rows-1 back from the arm as a CRC-32 and
// returns it with the CRC of what the library expects there. It first waits
// until the arm ran every command sent before, however long an upload takes
// while the arm makes progress (ErrNoReply after 15 s without progress, and
// while the panel is off).
func (a Arm) PanelCRC(y0, rows int) (got, want uint32, err error) {
	mu.RLock()
	defer mu.RUnlock()
	var g, w C.uint32_t
	if C.sk_g1_panel_crc(C.int(a), C.int(y0), C.int(rows), &g, &w) == 0 {
		return 0, 0, ErrNoReply
	}
	return uint32(g), uint32(w), nil
}

// ArenaCRC is the CRC-32 of arena bytes as the arm holds them and as the
// library expects them (length 0: to the end), after the commands sent before
// have run. ErrNoReply also for bytes a loaded program changes.
func (a Arm) ArenaCRC(address, length uint32) (got, want uint32, err error) {
	mu.RLock()
	defer mu.RUnlock()
	var g, w C.uint32_t
	if C.sk_g1_arena_crc(C.int(a), C.uint32_t(address), C.uint32_t(length), &g, &w) == 0 {
		return 0, 0, ErrNoReply
	}
	return uint32(g), uint32(w), nil
}

// ReadArena reads n bytes of the arm's arena (after the commands sent before
// have run), 16 per request.
func (a Arm) ReadArena(address uint32, n int) ([]byte, error) {
	b := make([]byte, n)
	if n == 0 {
		return b, nil
	}
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_read_arena(C.int(a), C.uint32_t(address), (*C.uint8_t)(unsafe.Pointer(&b[0])), C.size_t(n)) == 0 {
		return nil, ErrNoReply
	}
	return b, nil
}

// WriteArena queues WRITEs of the bytes at address.
func (a Arm) WriteArena(address uint32, data []byte) {
	if len(data) == 0 {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_write_arena(C.int(a), C.uint32_t(address), (*C.uint8_t)(unsafe.Pointer(&data[0])), C.size_t(len(data)))
}

// Raise sets a trigger pending (USER0...USER3 hand work to the program).
func (a Arm) Raise(t Trigger) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_raise(C.int(a), C.int(t))
}

// Notify queues NOTIFY x, y: an EventNotify once the arm ran everything
// before it.
func (a Arm) Notify(x, y uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_notify(C.int(a), C.uint32_t(x), C.uint32_t(y))
}

// SysWrite queues SYS_WRITE of a property.
func (a Arm) SysWrite(p Property, value uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_sys_write(C.int(a), C.int(p), C.uint32_t(value))
}

// Call runs stored code at address as part of a packet (VM.md §6.2).
func (a Arm) Call(address uint32) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_call(C.int(a), C.uint32_t(address))
}

// Run queues host code (a Program's Code without jumps) as one packet.
func (a Arm) Run(code []byte) {
	if len(code) == 0 {
		return
	}
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_run(C.int(a), (*C.uint8_t)(unsafe.Pointer(&code[0])), C.size_t(len(code)))
}

// Load queues the program's table, pool and code and marks what it changes.
func (a Arm) Load(p *Program) error {
	if err := p.Err(); err != nil {
		return err
	}
	mu.RLock()
	defer mu.RUnlock()
	ok := C.sk_g1_load(C.int(a), p.c)
	if ok == 0 {
		return fmt.Errorf("scouterkit: %s arm: load refused: %s", a, a.lastError())
	}
	return nil
}

// Start waits until the arm ran everything sent before, then starts the
// program whose trigger table is at table (VM_START).
func (a Arm) Start(table uint32) error {
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_vm_start(C.int(a), C.uint32_t(table)) == 0 {
		return fmt.Errorf("%w (VM_START on the %s arm)", ErrNoReply, a)
	}
	return nil
}

// Stop stops the program (VM_STOP).
func (a Arm) Stop() error {
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_vm_stop(C.int(a)) == 0 {
		return ErrNoReply
	}
	return nil
}

type VMStatus struct {
	State         int // 0 stopped, 1 running, 2 halted
	Trigger       int // of the current task, 0xFF none
	Pending       uint16
	Table         uint32
	Next          uint32 // the task's next instruction (halted: the failing one)
	Task          int    // 0 none, 1 running, 2 SLEEP_UNTIL, 3 WAIT_EVENT
	JumpsLeft     int
	Depth         int
	HaltCode      int
	Timer         uint32
	Tasks         uint32
	Coalesced     int
	NotifyDropped int
	CapPauses     int
	BusyMS        int
	TimerArmed    bool
}

func (a Arm) VMStatus() (VMStatus, error) {
	mu.RLock()
	defer mu.RUnlock()
	var s C.sk_g1_vm_status
	if C.sk_g1_get_vm_status(C.int(a), &s) == 0 {
		return VMStatus{}, ErrNoReply
	}
	return VMStatus{
		State: int(s.state), Trigger: int(s.trigger), Pending: uint16(s.pending), Table: uint32(s.table),
		Next: uint32(s.next), Task: int(s.task), JumpsLeft: int(s.jumps_left), Depth: int(s.depth),
		HaltCode: int(s.halt_code), Timer: uint32(s.timer), Tasks: uint32(s.tasks), Coalesced: int(s.coalesced),
		NotifyDropped: int(s.notify_dropped), CapPauses: int(s.cap_pauses), BusyMS: int(s.busy_ms),
		TimerArmed: s.timer_armed != 0,
	}, nil
}

// Props reads up to 12 properties (Unknown for values the arm does not have).
func (a Arm) Props(ids ...Property) ([]uint32, error) {
	if len(ids) == 0 || len(ids) > 12 {
		return nil, fmt.Errorf("scouterkit: %d properties (1...12 per request)", len(ids))
	}
	b := make([]C.uint8_t, len(ids))
	for i, id := range ids {
		b[i] = C.uint8_t(id)
	}
	v := make([]C.uint32_t, len(ids))
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_props(C.int(a), &b[0], C.int(len(ids)), &v[0]) == 0 {
		return nil, ErrNoReply
	}
	out := make([]uint32, len(ids))
	for i := range v {
		out[i] = uint32(v[i])
	}
	return out, nil
}

// ReportInputs asks the arm to report these triggers (bit i = trigger i) as
// EventInput, also after reconnecting. It returns the mask in force: the
// request and what this firmware can raise.
func (a Arm) ReportInputs(triggers uint16) (uint16, error) {
	mu.RLock()
	defer mu.RUnlock()
	var inForce C.uint16_t
	if C.sk_g1_report_inputs(C.int(a), C.uint16_t(triggers), &inForce) == 0 {
		return 0, ErrNoReply
	}
	return uint16(inForce), nil
}

type EventType int

const (
	EventError  EventType = 0x02 // the arm refused host code
	EventHalt   EventType = 0x03 // a program stopped: HALT or an error
	EventNotify EventType = 0x04 // NOTIFY by a program or host code
	EventInput  EventType = 0x07 // a trigger reported by ReportInputs
	EventReset  EventType = 0x80 // the arm lost its session: its program stopped, its memory was restored
)

type Event struct {
	Type     EventType
	Epoch    uint32
	DeviceUS uint32 // the arm's clock when it made the event
	HostUS   int64  // arrival, on NowUS's clock
	Trigger  int    // HALT: the task's; NOTIFY: its source (0xFF host code); INPUT: which
	Count    uint32 // NOTIFY, INPUT: the arm's counter; ERROR: the packet's seq
	A, B     uint32 // NOTIFY: its values; INPUT: A = the value
	Address  uint32 // HALT, ERROR: the failing instruction
	Op, Code int    // HALT, ERROR: its opcode and the error code (HALT: 0)
	Detail   uint32
	Offset   int // ERROR: of the instruction in its packet
}

// NextEvent returns the arm's oldest event, waiting up to timeout; false if
// none came. The library keeps the last 256.
func (a Arm) NextEvent(timeout time.Duration) (Event, bool) {
	deadline := time.Now().Add(timeout)
	for {
		wait := time.Until(deadline)
		if wait > 100*time.Millisecond {
			wait = 100 * time.Millisecond
		}
		if wait < 0 {
			wait = 0
		}
		var e C.sk_g1_event
		mu.RLock()
		got := C.sk_g1_next_event(C.int(a), &e, C.int(wait.Milliseconds()))
		mu.RUnlock()
		if got != 0 {
			return Event{
				Type: EventType(e._type), Epoch: uint32(e.epoch), DeviceUS: uint32(e.device_us),
				HostUS: int64(e.host_us), Trigger: int(e.trigger), Count: uint32(e.count), A: uint32(e.a),
				B: uint32(e.b), Address: uint32(e.address), Op: int(e.op), Code: int(e.code),
				Detail: uint32(e.detail), Offset: int(e.offset),
			}, true
		}
		if !time.Now().Before(deadline) {
			return Event{}, false
		}
	}
}

// DeviceTime is the arm's clock (µs, wraps) at NowUS() + in.
func (a Arm) DeviceTime(in time.Duration) uint32 {
	mu.RLock()
	defer mu.RUnlock()
	return uint32(C.sk_g1_device_time(C.int(a), C.int64_t(in.Microseconds())))
}

// Disconnect drops the link; the library connects again after d. The
// session, the arm's memory and its program stay.
func (a Arm) Disconnect(d time.Duration) {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_g1_disconnect(C.int(a), C.int(d.Milliseconds()))
}

func (a Arm) lastError() string {
	var s C.sk_g1_state
	C.sk_g1_get_state(C.int(a), &s)
	return C.GoString(&s.last_error[0])
}

// NowUS is the library's host clock (steady, µs).
func NowUS() int64 { return int64(C.sk_g1_now_us()) }
