package transport

import (
	"context"
	"net/http"
	"strconv"
	"time"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
)

const exportChunkSize = 64 << 10
const exportWriteTimeout = 2 * time.Second

// Probe capabilities without Flush: calling it would itself commit headers.
func exportCanFlush(w http.ResponseWriter) bool {
	for depth := 0; depth < 32; depth++ {
		switch writer := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = writer.Unwrap()
		default:
			return false
		}
	}
	return false
}

// Every check completes its transaction before Write. A committed revocation
// can therefore race with ONE already authorized chunk; accepted bytes cannot
// be recalled. After headers only an aborted response is safe, never JSON.
func deliverExport(ctx context.Context, w http.ResponseWriter, data []byte, run string, authorize func(context.Context) error) error {
	if !exportCanFlush(w) {
		return experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
	}
	controller := http.NewResponseController(w)
	committed := false
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	for offset := 0; offset < len(data); offset += exportChunkSize {
		err := ctx.Err()
		if err == nil {
			err = authorize(ctx)
		}
		if err != nil {
			if committed {
				panic(http.ErrAbortHandler)
			}
			if ctx.Err() != nil {
				return experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
			}
			return err
		}
		deadline := time.Now().Add(exportWriteTimeout)
		if total, ok := ctx.Deadline(); ok && total.Before(deadline) {
			deadline = total
		}
		if err = controller.SetWriteDeadline(deadline); err != nil {
			if committed {
				panic(http.ErrAbortHandler)
			}
			return experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
		}
		if !committed {
			w.Header().Set("Content-Type", "application/zip")
			w.Header().Set("Content-Disposition", `attachment; filename="run-`+run+`.zip"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusOK)
			committed = true
		}
		chunk := data[offset:min(offset+exportChunkSize, len(data))]
		n, err := w.Write(chunk)
		if err != nil || n != len(chunk) {
			panic(http.ErrAbortHandler)
		}
		if err = controller.Flush(); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
	return nil
}
