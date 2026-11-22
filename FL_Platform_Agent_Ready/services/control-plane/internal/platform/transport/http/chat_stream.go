package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/conversation"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

type chatRunner func(context.Context, string, string, int, string, []inference.ChatMessage, func(string) error) (string, *inference.Usage, error)

func (s *Server) streamChat(w http.ResponseWriter, r *http.Request, owner string, store conversation.Store, job conversation.Job, run chatRunner) {
	ctx, cancel := context.WithDeadline(r.Context(), job.Generation.Deadline)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(130 * time.Second))
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-store")
	send := func(event string, data any) error {
		raw, err := json.Marshal(data)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, raw); err != nil {
			return err
		}
		return controller.Flush()
	}
	// DB cancellation works even when another API replica handles the cancel request.
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				active, err := store.Active(ctx, owner, job.Generation.ID)
				if err != nil || !active {
					cancel()
					return
				}
			}
		}
	}()
	var content strings.Builder
	last := time.Now()
	emit := func(text string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		content.WriteString(text)
		if time.Since(last) >= 250*time.Millisecond {
			if err := store.Checkpoint(ctx, owner, job.Generation.ID, content.String()); err != nil {
				return err
			}
			last = time.Now()
		}
		return send("delta", map[string]string{"text": text})
	}
	reason := ""
	var usage *inference.Usage
	err := send("start", job.Generation)
	if err == nil {
		reason, usage, err = run(ctx, owner, job.ProviderID, job.Revision, job.Model, job.Messages, emit)
	}
	status, code := "completed", ""
	if err != nil {
		status, code = "failed", "GENERATION_FAILED"
		var upstream inference.Failure
		if errors.As(err, &upstream) {
			code = upstream.Code
		}
		if code == "PROVIDER_STREAM_INTERRUPTED" {
			status = "interrupted"
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status, code = "interrupted", "GENERATION_TIMEOUT"
		} else if ctx.Err() != nil {
			status, code = "cancelled", "GENERATION_CANCELLED"
		}
	}
	rawUsage, _ := json.Marshal(usage)
	finalCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
	defer finish()
	result, saveErr := store.Finish(finalCtx, owner, job.Generation.ID, content.String(), status, code, reason, rawUsage)
	if saveErr == nil {
		_ = send("done", result)
	}
	// A failed durable write deliberately has no done: the UI reloads the receipt.
}
