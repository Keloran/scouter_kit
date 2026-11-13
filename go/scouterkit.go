// Package scouterkit draws on Even Realities G1 glasses that run the scouter
// firmware. It wraps the ScouterKit C++ library in ../src through its C layer.
//
// Both eyes form one gray virtual display. Bitmaps live in the glasses'
// memory: create or load them, draw text into them, place them on the display
// with Draw, and show the result on both eyes at once with Flush.
//
// Pixels are 0 (off, see-through) to 255 (brightest); the G1 shows 16 levels,
// so bitmaps and the display hold multiples of 17. Every function is safe for
// concurrent use.
//
// Build with -tags emulator to talk to two emulated arms instead of BlueZ.
package scouterkit

/*
#cgo CPPFLAGS: -I${SRCDIR}/../src/c -DSCOUTER_G1_MIC
#cgo CXXFLAGS: -std=c++20
#cgo emulator CPPFLAGS: -DSCOUTER_G1_EMULATOR
#cgo !emulator pkg-config: libsystemd
#include <stdlib.h>
#include "scouter_kit_c.h"
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"

	_ "scouterkit/internal/lc3" // the microphone's LC3 decoder (SCOUTER_G1_MIC)
)

var (
	ErrBluetooth      = errors.New("scouterkit: Bluetooth error")
	ErrNoG1Found      = errors.New("scouterkit: no G1 found")
	ErrBonding        = errors.New("scouterkit: bonding failed")
	ErrFirmware       = errors.New("scouterkit: the arms do not run the scouter firmware")
	ErrNotInitialized = errors.New("scouterkit: not initialized")
	ErrNoReply        = errors.New("scouterkit: no reply from the arm")
	ErrCancelled      = errors.New("scouterkit: session cancelled")
)

// mu keeps handles valid: calls that use a handle hold it for reading, calls
// that free handles or copy pixels out of the library hold it for writing.
// generation changes whenever all handles become invalid.
var (
	mu          sync.RWMutex
	generation  int
	initialized bool
)

// Config chooses the glasses and the display geometry.
type Config struct {
	// The display is (640 + |DX|) x (480 - |DY|) pixels: the left eye shows
	// columns 0...639 on its panel rows 0...470, the right eye columns
	// DX...DX+639 on its rows DY...479 (for DX = 100, DY = 9).
	DX, DY int
	// Bluetooth addresses of the arms. Empty: the bonded glasses (or, with
	// the case open, glasses in range, which get bonded). "none" leaves the
	// arm out.
	Left, Right string
	// The BlueZ adapter, by name ("hci1") or address. Empty: the first
	// powered one. Bonds belong to an adapter: another one bonds anew.
	Adapter string
	// How long Init may take; 0 means 30 s.
	Timeout time.Duration
	// Keep the arms' sessions as they are: a program on them runs on and
	// their memory stays. The library then owns none of the arena (bitmaps,
	// fonts and Alloc fail) and draws nothing: for control requests and
	// events only, such as the microphone.
	KeepSessions bool
	// The library logs to stderr.
	Log bool
	// Optional one-shot signal for this session; never reused for a later Init.
	Cancellation *Cancellation
}

// DefaultConfig returns the user's calibration (DX 100, DY 9: a 740 x 471
// display) and the bonded glasses.
func DefaultConfig() Config {
	return Config{DX: 100, DY: 9}
}

// Init connects to the glasses and returns once both arms are ready. The
// error wraps ErrBluetooth, ErrNoG1Found, ErrBonding or ErrFirmware.
func Init(cfg Config) error {
	return InitContext(context.Background(), cfg)
}

// InitContext cancels initialization when ctx ends. After it returns, ctx no
// longer controls the session; Config.Cancellation may still stop its work.
func InitContext(ctx context.Context, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := lockForInit(ctx); err != nil {
		return err
	}
	defer mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if initialized {
		return fmt.Errorf("%w: already initialized", ErrBluetooth)
	}
	var token *Cancellation
	var err error
	if cfg.Cancellation == nil {
		token, err = NewCancellation()
	} else {
		token, err = cfg.Cancellation.clone()
	}
	if err != nil {
		return err
	}
	defer token.dispose()
	stopCancel := context.AfterFunc(ctx, token.Cancel)
	defer stopCancel()
	left := C.CString(cfg.Left)
	defer C.free(unsafe.Pointer(left))
	right := C.CString(cfg.Right)
	defer C.free(unsafe.Pointer(right))
	adapter := C.CString(cfg.Adapter)
	defer C.free(unsafe.Pointer(adapter))
	var msg [256]C.char
	c := C.sk_g1_config{dx: C.int(cfg.DX), dy: C.int(cfg.DY), left: left, right: right, adapter: adapter,
		timeout_ms: C.int(cfg.Timeout.Milliseconds())}
	if cfg.KeepSessions {
		c.keep_sessions = 1
	}
	if cfg.Log {
		c.log = 1
	}
	e := C.sk_g1_init_cancelable(&c, token.token, &msg[0], C.size_t(len(msg)))
	stopCancel()
	if ctx.Err() != nil || token.Cancelled() || e == C.SK_G1_ERROR_CANCELLED {
		token.Cancel()
		if e == C.SK_G1_ERROR_NONE {
			C.sk_g1_deinit()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrCancelled
	}
	if e == C.SK_G1_ERROR_NONE {
		initialized = true
		return nil
	}
	kind := ErrBluetooth
	switch e {
	case C.SK_G1_ERROR_NO_G1_FOUND:
		kind = ErrNoG1Found
	case C.SK_G1_ERROR_BONDING:
		kind = ErrBonding
	case C.SK_G1_ERROR_FIRMWARE:
		kind = ErrFirmware
	}
	return fmt.Errorf("%w: %s", kind, C.GoString(&msg[0]))
}

// Deinit waits until the last update is shown, disconnects and unloads all
// fonts and bitmaps.
func Deinit() {
	mu.Lock()
	defer mu.Unlock()
	C.sk_g1_deinit()
	initialized = false
	generation++
}

// Size returns the size of the display (0, 0 before Init).
func Size() (width, height int) {
	mu.RLock()
	defer mu.RUnlock()
	v := C.sk_display()
	return int(v.width), int(v.height)
}

// Draw places the bitmap on the display with its top left corner at (x, y);
// later calls draw on top. Flush shows it.
func Draw(b *Bitmap, x, y int) {
	mu.RLock()
	defer mu.RUnlock()
	if c := b.handle(); c != nil {
		C.sk_draw(c, C.int(x), C.int(y))
	}
}

// Clear places black over the whole display.
func Clear() {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_clear()
}

// Flush uploads the changed bitmap pixels and shows what Draw placed since
// the last Flush on both eyes at the same moment, with the bitmaps' contents
// at this time. It returns once the update is queued, after waiting for the
// previous update to be shown.
func Flush() {
	mu.RLock()
	defer mu.RUnlock()
	C.sk_flush()
}

// Display returns a copy of what the eyes show, as of the last Flush.
func Display() *image.Gray {
	mu.Lock()
	defer mu.Unlock()
	return copyView(C.sk_display())
}

// TotalMemory is the glasses' memory for fonts and bitmaps, in bytes per arm.
func TotalMemory() int {
	mu.RLock()
	defer mu.RUnlock()
	return int(C.sk_total_memory())
}

// FreeMemory is the part of TotalMemory that is still free.
func FreeMemory() int {
	mu.RLock()
	defer mu.RUnlock()
	return int(C.sk_free_memory())
}

// UnloadAll unloads every font and bitmap.
func UnloadAll() {
	mu.Lock()
	defer mu.Unlock()
	C.sk_unload_all()
	generation++
}

func copyView(v C.sk_view) *image.Gray {
	w, h, stride := int(v.width), int(v.height), int(v.stride)
	img := image.NewGray(image.Rect(0, 0, w, h))
	if v.data == nil || w <= 0 || h <= 0 {
		return img
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(v.data)), (h-1)*stride+w)
	for y := 0; y < h; y++ {
		copy(img.Pix[y*img.Stride:y*img.Stride+w], src[y*stride:y*stride+w])
	}
	return img
}
