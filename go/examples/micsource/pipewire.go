package main

/*
#cgo pkg-config: libpipewire-0.3
#cgo CFLAGS: -D_GNU_SOURCE
#include <stdlib.h>
#include "pipewire.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// source is a PipeWire Audio/Source node that plays the samples written to it.
type source struct{ c *C.mic_source }

type sourceStats struct {
	State     int // -1 error, 0 unconnected, 1 connecting, 2 paused, 3 streaming
	NodeID    uint32
	Fill      int     // samples queued
	Rate      float64 // the correction given to the stream's resampler
	Adaptive  bool
	Cycles    uint64
	Played    uint64 // samples
	Silence   uint64 // samples of silence: filling up, underruns
	Underruns uint64
	Overruns  uint64
	Skipped   uint64 // samples dropped to get back to the target
}

// newSource creates the node; target is the number of samples kept queued.
func newSource(name, description string, rate, target int) (*source, error) {
	cname, cdesc := C.CString(name), C.CString(description)
	defer C.free(unsafe.Pointer(cname))
	defer C.free(unsafe.Pointer(cdesc))
	var msg [160]C.char
	c := C.mic_source_new(cname, cdesc, C.int(rate), C.int(target), &msg[0], C.size_t(len(msg)))
	if c == nil {
		return nil, errors.New(C.GoString(&msg[0]))
	}
	if C.mic_source_wait(c, 3000, &msg[0], C.size_t(len(msg))) == 0 {
		C.mic_source_free(c)
		return nil, errors.New(C.GoString(&msg[0]))
	}
	return &source{c}, nil
}

// Write queues samples; while nothing records from the node they are dropped.
func (s *source) Write(pcm []int16) {
	if len(pcm) > 0 {
		C.mic_source_write(s.c, (*C.int16_t)(unsafe.Pointer(&pcm[0])), C.int(len(pcm)))
	}
}

func (s *source) Stats() sourceStats {
	var st C.mic_source_stats
	C.mic_source_get_stats(s.c, &st)
	return sourceStats{State: int(st.state), NodeID: uint32(st.node_id), Fill: int(st.fill), Rate: float64(st.rate),
		Adaptive: st.adaptive != 0, Cycles: uint64(st.cycles), Played: uint64(st.played), Silence: uint64(st.silence),
		Underruns: uint64(st.underruns), Overruns: uint64(st.overruns), Skipped: uint64(st.skipped)}
}

// Close removes the node.
func (s *source) Close() {
	C.mic_source_free(s.c)
	s.c = nil
}

// capture records from a node, for the checks; the samples stay in memory.
type capture struct{ c *C.mic_capture }

func newCapture(target string, rate int) (*capture, error) {
	ctarget := C.CString(target)
	defer C.free(unsafe.Pointer(ctarget))
	var msg [160]C.char
	c := C.mic_capture_new(ctarget, C.int(rate), &msg[0], C.size_t(len(msg)))
	if c == nil {
		return nil, errors.New(C.GoString(&msg[0]))
	}
	return &capture{c}, nil
}

// Read returns up to len(buf) samples recorded since the last call.
func (c *capture) Read(buf []int16) int {
	if len(buf) == 0 {
		return 0
	}
	return int(C.mic_capture_read(c.c, (*C.int16_t)(unsafe.Pointer(&buf[0])), C.int(len(buf))))
}

func (c *capture) Close() {
	C.mic_capture_free(c.c)
	c.c = nil
}
