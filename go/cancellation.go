package scouterkit

/*
#include "scouter_kit_c.h"
*/
import "C"

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Cancellation is a one-shot signal for the Init/session given this token in
// Config. Cancel fails pending work and stops the transport between callbacks;
// Deinit must still join cleanup and invalidate handles. It never targets a
// subsequent session. Close cancels and frees this handle; native copies keep
// their own reference to its signal. Every method is safe concurrently.
type Cancellation struct {
	mu    sync.Mutex
	token *C.sk_g1_cancellation
}

func NewCancellation() (*Cancellation, error) {
	p := C.sk_g1_cancellation_new()
	if p == nil {
		return nil, errors.New("scouterkit: cannot allocate cancellation token")
	}
	return &Cancellation{token: p}, nil
}

func (c *Cancellation) Cancel() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != nil {
		C.sk_g1_cancellation_cancel(c.token)
	}
}

func (c *Cancellation) Cancelled() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token == nil || C.sk_g1_cancellation_cancelled(c.token) != 0
}

func (c *Cancellation) Close() {
	if c == nil {
		return
	}
	c.Cancel()
	c.dispose()
}

// dispose relinquishes a handle without signalling its native session.
func (c *Cancellation) dispose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	C.sk_g1_cancellation_free(c.token)
	c.token = nil
}

func (c *Cancellation) clone() (*Cancellation, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token == nil {
		return nil, ErrCancelled
	}
	p := C.sk_g1_cancellation_clone(c.token)
	if p == nil {
		return nil, errors.New("scouterkit: cannot copy cancellation token")
	}
	return &Cancellation{token: p}, nil
}

func lockForInit(ctx context.Context) error {
	if ctx.Done() == nil {
		mu.Lock()
		return nil
	}
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !mu.TryLock() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
	return nil
}
