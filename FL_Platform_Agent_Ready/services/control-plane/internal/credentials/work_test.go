package credentials

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func workResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("work did not return; possible waiting queue or leaked permit")
		return nil
	}
}

func TestWorkBusyCancellationRetainsPermitUntilActualReturn(t *testing.T) {
	var pool WorkPool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	done := make(chan error, 1)
	go func() {
		done <- pool.Do(ctx, func(received context.Context) error {
			if received != ctx {
				return errors.New("caller context replaced")
			}
			close(started)
			<-release
			return nil
		})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("initial work did not start")
	}
	cancel()
	var calls atomic.Int32
	for i := 0; i < 8; i++ {
		attempt := make(chan error, 1)
		go func() {
			attempt <- pool.Do(context.Background(), func(context.Context) error { calls.Add(1); return nil })
		}()
		if err := workResult(t, attempt); !errors.Is(err, ErrWorkBusy) {
			t.Fatalf("cancelled active work lost permit: %v", err)
		}
	}
	select {
	case <-done:
		t.Fatal("Do returned while callback was still executing")
	default:
	}
	if calls.Load() != 0 {
		t.Fatal("busy attempt invoked callback")
	}
	once.Do(func() { close(release) })
	if err := workResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled sequence returned %v", err)
	}
	if err := pool.Do(context.Background(), func(context.Context) error { calls.Add(1); return nil }); err != nil || calls.Load() != 1 {
		t.Fatal("actual return did not release permit", err)
	}
}

func TestWorkSequenceKeepsPermitAndStopsBeforeSecondStep(t *testing.T) {
	var pool WorkPool
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	second := false
	err := pool.Do(ctx, func(context.Context) error {
		if nested := pool.Do(context.Background(), func(context.Context) error { return nil }); !errors.Is(nested, ErrWorkBusy) {
			t.Fatal("sequence did not retain permit", nested)
		}
		cancel()
		return nil
	}, func(context.Context) error { second = true; return nil })
	if !errors.Is(err, context.Canceled) || second {
		t.Fatal("cancellation started next Hash/commit step", err)
	}
	badPassword := errors.New("test credential denial")
	err = pool.Do(context.Background(), func(context.Context) error { return badPassword }, func(context.Context) error { second = true; return nil })
	if !errors.Is(err, badPassword) || second {
		t.Fatal("first failure did not stop sequence", err)
	}
	var order []int
	forStep := func(n int) func(context.Context) error {
		return func(context.Context) error {
			if nested := pool.Do(context.Background(), func(context.Context) error { return nil }); !errors.Is(nested, ErrWorkBusy) {
				t.Fatal("step lost permit", n, nested)
			}
			order = append(order, n)
			return nil
		}
	}
	if err = pool.Do(context.Background(), forStep(1), forStep(2)); err != nil || len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatal("ordered success after failure", err, order)
	}
}

func TestWorkInvalidAndExpiredInputsDoNotStartWork(t *testing.T) {
	var pool WorkPool
	called := false
	step := func(context.Context) error { called = true; return nil }
	var absent *WorkPool
	for _, err := range []error{absent.Do(context.Background(), step), pool.Do(nil, step), pool.Do(context.Background()), pool.Do(context.Background(), step, nil)} {
		if !errors.Is(err, ErrInvalidWork) {
			t.Fatal("invalid setup did not fail closed", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pool.Do(cancelled, step); !errors.Is(err, context.Canceled) {
		t.Fatal("pre-cancelled context", err)
	}
	expired, stop := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer stop()
	if err := pool.Do(expired, step); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("expired context", err)
	}
	if called {
		t.Fatal("invalid or cancelled input started work")
	}
	if err := pool.Do(context.Background(), step); err != nil || !called {
		t.Fatal("validation leaked permit", err)
	}
}

func TestWorkPanicPropagatesAndReleasesPermit(t *testing.T) {
	var pool WorkPool
	marker := &struct{}{}
	func() {
		defer func() {
			if recover() != marker {
				t.Fatal("work panic swallowed or changed")
			}
		}()
		_ = pool.Do(context.Background(), func(context.Context) error { panic(marker) })
		t.Fatal("panic did not propagate")
	}()
	if err := pool.Do(context.Background(), func(context.Context) error { return nil }); err != nil {
		t.Fatal("panic leaked permit", err)
	}
}

func TestWorkConcurrentAttemptsHaveOneWinnerAndNoQueue(t *testing.T) {
	var pool WorkPool
	const attempts = 48
	start, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	done := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		go func() {
			<-start
			done <- pool.Do(context.Background(), func(context.Context) error {
				calls.Add(1)
				<-release
				return nil
			})
		}()
	}
	close(start)
	for i := 0; i < attempts-1; i++ {
		if err := workResult(t, done); !errors.Is(err, ErrWorkBusy) {
			t.Fatal("non-winner was queued or admitted", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent callback count", calls.Load())
	}
	once.Do(func() { close(release) })
	if err := workResult(t, done); err != nil {
		t.Fatal("winner failed", err)
	}
}
