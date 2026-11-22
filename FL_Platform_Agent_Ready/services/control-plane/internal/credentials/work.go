package credentials

import (
	"context"
	"errors"
	"sync/atomic"
)

// Work errors contain no credentials or caller-supplied values. Public adapters
// map these operational failures to503; they are not invalid-password results.
var (
	ErrWorkBusy    = errors.New("credential work unavailable")
	ErrInvalidWork = errors.New("invalid credential work")
)

// WorkPool permits one synchronous credential sequence with no waiting queue.
// Its zero value is ready to use. Construct one shared pointer per API process
// and pass it to every credential path; do not copy it after first use or create
// a pool per request. Multiple API processes each have their own single permit.
type WorkPool struct{ held atomic.Bool }

// Do tries to run all steps under one permit, in order, on the caller's goroutine.
// Cancellation stops subsequent steps, but cannot interrupt an active Argon call.
// A cancelled caller therefore retains the permit until that step actually
// returns. No detached goroutine, timer release or internal queue is involved.
//
// Steps may capture results locally, but must not commit them: callers still
// recheck the original context and current credentials/permissions in their final
// transaction. A context can be cancelled immediately after Do returns too.
// Step failures stop the sequence. Panics propagate after releasing the permit.
func (p *WorkPool) Do(ctx context.Context, steps ...func(context.Context) error) error {
	if p == nil || ctx == nil || len(steps) == 0 {
		return ErrInvalidWork
	}
	for _, step := range steps {
		if step == nil {
			return ErrInvalidWork
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.held.CompareAndSwap(false, true) {
		return ErrWorkBusy
	}
	defer p.held.Store(false)
	for _, step := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := step(ctx)
		if cancelled := ctx.Err(); cancelled != nil {
			return cancelled
		}
		if err != nil {
			return err
		}
	}
	return nil
}
