//go:build integration

package transport

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/conversation"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

func TestChatLifecycle(t *testing.T) {
	pool := aiDatabase(t)
	ctx := context.Background()
	owner, other, provider := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,'fixture','researcher')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version) VALUES($1,$2,'fixture','https://models.example/v1','cipher','v1')`, provider, owner); err != nil {
		t.Fatal(err)
	}
	store := conversation.Store{Pool: pool}
	if _, err := store.Create(ctx, owner, provider, "model", false); err == nil {
		t.Fatal("consent bypass")
	}
	if _, err := store.Create(ctx, other, provider, "model", true); err == nil {
		t.Fatal("foreign provider")
	}
	c, err := store.Create(ctx, owner, provider, "model", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Detail(ctx, other, c.ID); err == nil {
		t.Fatal("foreign history")
	}
	id := uuid.NewString()
	job, err := store.Begin(ctx, owner, c.ID, id, "Hello", "fingerprint")
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := store.Begin(ctx, owner, c.ID, id, "Hello", "fingerprint")
	if err != nil || !repeat.Replay {
		t.Fatal("replay failure", err)
	}
	if _, err = store.Begin(ctx, owner, c.ID, id, "Other", "different"); err == nil {
		t.Fatal("conflict accepted")
	}
	if _, err = store.Begin(ctx, owner, c.ID, uuid.NewString(), "Parallel", "p"); err == nil {
		t.Fatal("parallel generation accepted")
	}
	s := Server{}
	r := httptest.NewRequest("POST", "/", nil)
	w := httptest.NewRecorder()
	s.streamChat(&observedWriter{ResponseWriter: w}, r, owner, store, job, func(ctx context.Context, _ string, _ string, _ int, _ string, m []inference.ChatMessage, emit func(string) error) (string, *inference.Usage, error) {
		if len(m) != 1 || m[0].Role != "user" {
			t.Fatal("bad history")
		}
		if err := emit("Answer"); err != nil {
			return "", nil, err
		}
		return "stop", nil, nil
	})
	if !w.Flushed || !strings.Contains(w.Body.String(), "event: delta") || !strings.Contains(w.Body.String(), "event: done") {
		t.Fatal("stream not flushed", w.Body.String())
	}
	_, items, err := store.Detail(ctx, owner, c.ID)
	if err != nil || len(items) != 1 || items[0].Content != "Answer" || items[0].Status != "completed" {
		t.Fatal(items, err)
	}
	next, err := store.Begin(ctx, owner, c.ID, uuid.NewString(), "Follow up", "f2")
	if err != nil || len(next.Messages) != 3 {
		t.Fatal("history missing", err)
	}
	if err = store.Checkpoint(ctx, owner, next.Generation.ID, "Partial"); err != nil {
		t.Fatal(err)
	}
	if err = store.Cancel(ctx, owner, c.ID, false); err != nil {
		t.Fatal(err)
	}
	final, err := store.Finish(ctx, owner, next.Generation.ID, "Late answer", "completed", "", "stop", nil)
	if err != nil || final.Status != "cancelled" || final.Content != "Partial" {
		t.Fatal("late write won", final, err)
	}
	// Competing API workers share DB locks and the unique active-user index.
	var wg sync.WaitGroup
	var mu sync.Mutex
	success := 0
	var active conversation.Job
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, e := store.Begin(ctx, owner, c.ID, uuid.NewString(), "Concurrent", "concurrent")
			mu.Lock()
			defer mu.Unlock()
			if e == nil {
				success++
				active = j
			} else {
				var f inference.Failure
				if !errors.As(e, &f) || f.Code != "GENERATION_ACTIVE" {
					t.Error(e)
				}
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatal("concurrent success count", success)
	}
	if _, err = pool.Exec(ctx, `UPDATE ai_generations SET deadline=now()-interval '1 second' WHERE request_id=$1`, active.Generation.ID); err != nil {
		t.Fatal(err)
	}
	if err = store.Reap(ctx, owner); err != nil {
		t.Fatal(err)
	}
	final, err = store.Finish(ctx, owner, active.Generation.ID, "late", "completed", "", "stop", nil)
	if err != nil || final.Status != "interrupted" {
		t.Fatal("crash recovery failed", err)
	}
	// A cancel handled by another API instance interrupts this instance's upstream.
	remote, err := store.Begin(ctx, owner, c.ID, uuid.NewString(), "Remote cancel", "remote")
	if err != nil {
		t.Fatal(err)
	}
	started, finished := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		s.streamChat(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil), owner, store, remote,
			func(ctx context.Context, _ string, _ string, _ int, _ string, _ []inference.ChatMessage, _ func(string) error) (string, *inference.Usage, error) {
				close(started)
				<-ctx.Done()
				return "", nil, ctx.Err()
			})
	}()
	<-started
	otherReplica := conversation.Store{Pool: pool}
	if err = otherReplica.Cancel(ctx, owner, c.ID, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream ignored database cancellation")
	}
	if err = store.Cancel(ctx, owner, c.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.Detail(ctx, owner, c.ID); err == nil {
		t.Fatal("deleted dialog visible")
	}
	if _, err = store.Begin(ctx, owner, c.ID, id, "Hello", "fingerprint"); err == nil {
		t.Fatal("deleted replay visible")
	}
	var remaining int
	if err = pool.QueryRow(ctx, `SELECT sum(length(input)+length(content)) FROM ai_generations WHERE conversation_id=$1`, c.ID).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("deleted content retained", remaining, err)
	}
}
