//go:build emulator

package scouterkit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestInitContextCancelledBeforeLock(t *testing.T) {
	mu.RLock()
	defer mu.RUnlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- InitContext(ctx, DefaultConfig()) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled Init waited for the library lock")
	}
}

func TestCancellationRetiresOnlyItsSession(t *testing.T) {
	old, err := NewCancellation()
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	cfg := DefaultConfig()
	cfg.Cancellation = old
	if err := Init(cfg); err != nil {
		t.Fatal(err)
	}
	old.Cancel()
	deadline := time.Now().Add(time.Second)
	for Left.State().Connected && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if Left.State().Connected {
		t.Fatal("cancel did not stop the session")
	}
	Deinit()
	var users sync.WaitGroup
	for i := 0; i < 8; i++ {
		users.Add(1)
		go func() {
			defer users.Done()
			for j := 0; j < 100; j++ {
				old.Cancel()
				old.Close()
			}
		}()
	}
	users.Wait()
	if err := Init(cfg); !errors.Is(err, ErrCancelled) {
		t.Fatalf("closed token reused: %v", err)
	}
	if err := Init(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer Deinit()
	old.Cancel()
	time.Sleep(120 * time.Millisecond)
	if !Left.State().Connected || !Right.State().Connected {
		t.Fatal("old token stopped a later session")
	}
}

func TestInitContextEndsAtReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := InitContext(ctx, DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	defer Deinit()
	cancel()
	time.Sleep(120 * time.Millisecond)
	if !Left.State().Connected || !Right.State().Connected {
		t.Fatal("completed Init's context stopped its session")
	}
}

func TestInitWithCancelledToken(t *testing.T) {
	token, err := NewCancellation()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	token.Cancel()
	cfg := DefaultConfig()
	cfg.Cancellation = token
	if err := Init(cfg); !errors.Is(err, ErrCancelled) {
		t.Fatalf("cancel before native init: %v", err)
	}
	if err := Init(DefaultConfig()); err != nil {
		t.Fatal(err)
	}
	Deinit()
}
