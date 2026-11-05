package scouterkit

/*
#include "scouter_kit_c.h"
*/
import "C"

import (
	"fmt"
	"sync"
)

// The microphone's audio: 16 kHz mono, 10 ms frames.
const (
	MicSampleRate   = 16000
	MicFrameSamples = 160
)

// MicRate is the LC3 bitrate on the link.
type MicRate int

const (
	Mic32kbps MicRate = iota // 40-byte frames
	Mic16kbps                // 20-byte frames
)

// MicStopReason says why the microphone stopped.
type MicStopReason int

const (
	MicStoppedByHost   MicStopReason = iota // MicStop, or never started
	MicStoppedPanelOff                      // a panel fault (a PANEL = 0 no longer stops it, VM.md §6.5)
	MicStoppedFault                         // a capture fault on the arm
	MicStoppedLink                          // the link dropped
	MicStoppedLease                         // no renewal reached the arm for 2 s (the host stopped reading)
)

func (r MicStopReason) String() string {
	switch r {
	case MicStoppedPanelOff:
		return "a panel fault"
	case MicStoppedFault:
		return "a capture fault on the arm"
	case MicStoppedLink:
		return "the link dropped"
	case MicStoppedLease:
		return "the arm's lease ran out"
	}
	return "stopped by the host"
}

// MicFrame is 10 ms of the microphone, decoded.
type MicFrame struct {
	Counter   uint32 // frames since MicStart
	Concealed bool   // lost on the link or undecodable: the decoder's concealment
	DeviceUS  uint32 // the arm's clock when it sent the frame
	HostUS    int64  // arrival, on NowUS's clock
	PCM       [MicFrameSamples]int16
}

type MicState struct {
	On             bool
	Stopped        MicStopReason // why it last stopped
	FrameBytes     int           // the arm's reply to MIC
	FramesPerEvent int
	Events, Frames uint64 // received since MicStart
	Gaps, Missing  uint64 // counter gaps and the frames they missed (concealed)
	Bad            uint64 // frames the decoder could not decode (concealed)
	Dropped        uint64 // frames not read in time
}

type micReader struct{ done, exited chan struct{} }

var (
	micMu      sync.Mutex
	micReaders [2]*micReader
)

// stopMicReader ends the arm's reader goroutine, if any; micMu is held.
func stopMicReader(a Arm) {
	if r := micReaders[a&1]; r != nil {
		close(r.done)
		<-r.exited
		micReaders[a&1] = nil
	}
}

// MicStart switches the arm's microphone on (VM.md §6.5; on the G1 the right
// arm, State().Microphone) and returns its frames in order, decoded, with
// missing ones concealed. While it is on, the arm shows its indicator, a
// 16 x 16 block in the panel's outer top corner (the panel goes on and stays
// on: a PANEL = 0 meanwhile blanks it to the indicator). The channel closes
// when the microphone stops: MicStop, the link dropping, a fault or the lease
// running out (MicState().Stopped says which); the arm never starts again by
// itself. Read the channel promptly: the library keeps 2 s of frames.
func (a Arm) MicStart(rate MicRate) (<-chan MicFrame, error) {
	micMu.Lock()
	defer micMu.Unlock()
	stopMicReader(a)
	if err := a.micStart(rate); err != nil {
		return nil, err
	}
	r := &micReader{make(chan struct{}), make(chan struct{})}
	micReaders[a&1] = r
	frames := make(chan MicFrame, 10)
	go a.readMic(frames, r)
	return frames, nil
}

func (a Arm) micStart(rate MicRate) error {
	mu.RLock()
	defer mu.RUnlock()
	if !initialized {
		return ErrNotInitialized
	}
	if C.sk_g1_mic_start(C.int(a), C.int(rate)) != 0 {
		return nil
	}
	var s C.sk_g1_state
	C.sk_g1_get_state(C.int(a), &s)
	if s.present == 0 {
		return fmt.Errorf("scouterkit: MicStart: the %s arm is not configured", a)
	}
	return fmt.Errorf("scouterkit: MicStart: %s", C.GoString(&s.last_error[0]))
}

func (a Arm) readMic(frames chan<- MicFrame, r *micReader) {
	defer close(r.exited)
	defer close(frames)
	var f C.sk_g1_mic_frame
	for {
		select {
		case <-r.done:
			return
		default:
		}
		mu.RLock()
		got := C.sk_g1_mic_read(C.int(a), &f, 100) != 0
		var s C.sk_g1_mic_state
		if !got {
			C.sk_g1_mic_get_state(C.int(a), &s)
		}
		mu.RUnlock()
		if !got {
			if s.on == 0 {
				return
			}
			continue
		}
		frame := MicFrame{Counter: uint32(f.counter), Concealed: f.concealed != 0, DeviceUS: uint32(f.device_us),
			HostUS: int64(f.host_us)}
		for i := range frame.PCM {
			frame.PCM[i] = int16(f.pcm[i])
		}
		select {
		case frames <- frame:
		case <-r.done:
			return
		}
	}
}

// MicStop switches the microphone off, and the panel too if the library
// expects it off.
func (a Arm) MicStop() error {
	micMu.Lock()
	stopMicReader(a)
	micMu.Unlock()
	mu.RLock()
	defer mu.RUnlock()
	if C.sk_g1_mic_stop(C.int(a)) == 0 {
		return ErrNoReply
	}
	return nil
}

func (a Arm) MicState() MicState {
	mu.RLock()
	defer mu.RUnlock()
	var s C.sk_g1_mic_state
	C.sk_g1_mic_get_state(C.int(a), &s)
	return MicState{On: s.on != 0, Stopped: MicStopReason(s.stopped), FrameBytes: int(s.frame_bytes),
		FramesPerEvent: int(s.frames_per_event), Events: uint64(s.events), Frames: uint64(s.frames),
		Gaps: uint64(s.gaps), Missing: uint64(s.missing), Bad: uint64(s.bad), Dropped: uint64(s.dropped)}
}
