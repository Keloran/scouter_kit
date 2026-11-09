package main

import (
	"context"
	"errors"
	"math"
	"os"
	"syscall"
	"time"
	"unsafe"
)

type calibrationKey byte

const (
	calibrationUp calibrationKey = iota + 1
	calibrationDown
	calibrationRight
	calibrationLeft
	calibrationSave
	calibrationQuit
	calibrationLarger
	calibrationSmaller
)

type calibrationInput struct {
	key calibrationKey
	err error
}

type calibrationKeyParser struct {
	escape    int
	parameter string
}

// Escape sequences may arrive in separate reads. Accept both CSI and SS3 arrows.
func (p *calibrationKeyParser) feed(b byte) calibrationKey {
	if b == 27 {
		p.escape = 1
		p.parameter = ""
		return 0
	}
	switch p.escape {
	case 1:
		p.escape = 0
		if b == '[' || b == 'O' {
			p.escape = 2
		}
		return 0
	case 2:
		if b >= 0x40 && b <= 0x7e {
			p.escape = 0
			switch b {
			case 'A':
				return calibrationUp
			case 'B':
				return calibrationDown
			case 'C':
				return calibrationRight
			case 'D':
				return calibrationLeft
			case '~':
				if p.parameter == "5" {
					return calibrationLarger
				}
				if p.parameter == "6" {
					return calibrationSmaller
				}
			}
		} else if len(p.parameter) < 16 {
			p.parameter += string(b)
		}
		return 0
	}
	switch b {
	case 's', 'S':
		return calibrationSave
	case 'q', 'Q', 3:
		return calibrationQuit
	}
	return 0
}

func nudgeCalibration(o options, key calibrationKey) options {
	switch key {
	case calibrationUp:
		o.dy--
	case calibrationDown:
		o.dy++
	case calibrationRight:
		o.dx--
	case calibrationLeft:
		o.dx++
	case calibrationLarger:
		o.fontScale = min(maxFontScale, math.Round((o.fontScale+0.1)*10)/10)
	case calibrationSmaller:
		o.fontScale = max(minFontScale, math.Round((o.fontScale-0.1)*10)/10)
	}
	return o
}

func calibrationIOCTL(fd uintptr, request uintptr, arg *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(unsafe.Pointer(arg)))
	if errno != 0 {
		return errno
	}
	return nil
}

// Keep signal processing and output formatting intact; read keys without Enter.
// A 100ms read timeout lets cleanup stop the reader before restoring the terminal.
func calibrationKeyboard(parent context.Context, input *os.File) (<-chan calibrationInput, func(), error) {
	fd := input.Fd()
	var old syscall.Termios
	if err := calibrationIOCTL(fd, syscall.TCGETS, &old); err != nil {
		if errors.Is(err, syscall.ENOTTY) {
			return nil, func() {}, nil
		}
		return nil, nil, err
	}
	mode := old
	mode.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.IEXTEN
	mode.Iflag &^= syscall.ICRNL | syscall.IXON
	mode.Cc[syscall.VMIN], mode.Cc[syscall.VTIME] = 0, 1
	if err := calibrationIOCTL(fd, syscall.TCSETS, &mode); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	keys := make(chan calibrationInput, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(keys)
		var parser calibrationKeyParser
		var buf [64]byte
		for ctx.Err() == nil {
			n, err := syscall.Read(int(fd), buf[:])
			if errors.Is(err, syscall.EINTR) {
				continue
			}
			if errors.Is(err, syscall.EAGAIN) {
				select {
				case <-ctx.Done():
					return
				case <-time.After(10 * time.Millisecond):
					continue
				}
			}
			if err != nil {
				select {
				case keys <- calibrationInput{err: err}:
				case <-ctx.Done():
				}
				return
			}
			for _, b := range buf[:n] {
				if key := parser.feed(b); key != 0 {
					select {
					case keys <- calibrationInput{key: key}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return keys, func() {
		cancel()
		<-done
		if err := calibrationIOCTL(fd, syscall.TCSETS, &old); err != nil {
			progress("Could not restore terminal settings: %v", err)
		}
	}, nil
}
