package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
)

type exportTestWriter struct {
	*httptest.ResponseRecorder
	deadline   time.Time
	afterWrite func()
	writeError error
	slow       bool
}

// Deliberately has a deadline but neither Flush nor Unwrap.
type exportDeadlineOnly struct{ recorder *httptest.ResponseRecorder }

func (w *exportDeadlineOnly) Header() http.Header              { return w.recorder.Header() }
func (w *exportDeadlineOnly) WriteHeader(status int)           { w.recorder.WriteHeader(status) }
func (w *exportDeadlineOnly) Write(p []byte) (int, error)      { return w.recorder.Write(p) }
func (w *exportDeadlineOnly) SetWriteDeadline(time.Time) error { return nil }

func (w *exportTestWriter) SetWriteDeadline(d time.Time) error { w.deadline = d; return nil }
func (w *exportTestWriter) Write(p []byte) (int, error) {
	if w.slow {
		time.Sleep(max(0, time.Until(w.deadline)))
		return 0, context.DeadlineExceeded
	}
	if w.writeError != nil {
		return 0, w.writeError
	}
	n, err := w.ResponseRecorder.Write(p)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return n, err
}

func TestExportDeliveryInterruptions(t *testing.T) {
	for _, mode := range []string{"revoked", "dependency", "cancelled", "write", "slow"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
			defer cancel()
			w := &exportTestWriter{ResponseRecorder: httptest.NewRecorder()}
			checks := 0
			authorize := func(context.Context) error {
				checks++
				if checks > 1 {
					if mode == "revoked" {
						return experiment.Failure{Status: 404, Code: "NOT_FOUND"}
					}
					if mode == "dependency" {
						return experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
					}
				}
				return nil
			}
			if mode == "cancelled" {
				w.afterWrite = cancel
			}
			if mode == "write" {
				w.writeError = io.ErrClosedPipe
			}
			if mode == "slow" {
				w.slow = true
			}
			start := time.Now()
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				_ = deliverExport(ctx, w, make([]byte, 3*exportChunkSize), "fixture", authorize)
			}()
			if recovered != http.ErrAbortHandler || w.Body.Len() > exportChunkSize || time.Since(start) > 3*time.Second {
				t.Fatalf("delivery did not abort within budget: %v bytes=%d", recovered, w.Body.Len())
			}
		})
	}
}

func TestExportDeliveryIncompleteHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks := 0
		err := deliverExport(r.Context(), w, bytes.Repeat([]byte("x"), 3*exportChunkSize), "fixture", func(context.Context) error {
			checks++
			if checks == 2 {
				return experiment.Failure{Status: 401, Code: "UNAUTHORIZED"}
			}
			return nil
		})
		if err != nil {
			t.Errorf("unexpected preheaders error: %v", err)
		}
	}))
	defer server.Close()
	response, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || len(data) != exportChunkSize || response.StatusCode != 200 {
		t.Fatalf("client accepted incomplete response: status=%d bytes=%d err=%v", response.StatusCode, len(data), err)
	}
}

func TestExportDeliveryPreheadersAndZIPBudgets(t *testing.T) {
	deadlineOnly := &exportDeadlineOnly{recorder: httptest.NewRecorder()}
	observed := &observedWriter{ResponseWriter: deadlineOnly}
	err := deliverExport(context.Background(), observed, []byte("zip"), "fixture", func(context.Context) error { return nil })
	var f experiment.Failure
	if !errors.As(err, &f) || f.Code != "DEPENDENCY_UNAVAILABLE" || observed.status != 0 || deadlineOnly.recorder.Body.Len() != 0 || deadlineOnly.Header().Get("Content-Type") != "" {
		t.Fatal("unsupported flush committed ZIP headers", err)
	}
	w := httptest.NewRecorder()
	err = deliverExport(context.Background(), w, []byte("zip"), "fixture", func(context.Context) error { return nil })
	if !errors.As(err, &f) || f.Code != "DEPENDENCY_UNAVAILABLE" || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
		t.Fatal("unsupported deadline committed headers", err)
	}
	w = httptest.NewRecorder()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = deliverExport(ctx, w, []byte("zip"), "fixture", func(context.Context) error { t.Fatal("cancelled request called auth"); return nil })
	if err == nil || w.Body.Len() != 0 {
		t.Fatal("cancelled request published body")
	}
	files := map[string][]byte{"model.bin": bytes.Repeat([]byte("contents"), 1000)}
	budget := zipBudget{ctx: context.Background(), limit: 100}
	for _, size := range []int{60, 30} {
		if _, err = budget.Write(make([]byte, size)); err != nil || cap(budget.data) > 100 {
			t.Fatal("ZIP capacity exceeded limit", err, cap(budget.data))
		}
	}
	if n, e := budget.Write(make([]byte, 11)); n != 0 || e == nil || len(budget.data) != 90 || cap(budget.data) > 100 {
		t.Fatal("ZIP overflow changed retained buffer")
	}
	archive, err := buildExportZIP(context.Background(), files, 20)
	if !errors.As(err, &f) || f.Code != "EXPORT_LIMIT_EXCEEDED" || archive != nil {
		t.Fatal("ZIP budget silently truncated", err)
	}
	if archive, err = buildExportZIP(ctx, files, exportZIPLimit); err == nil || archive != nil {
		t.Fatal("cancelled ZIP returned bytes")
	}
}
