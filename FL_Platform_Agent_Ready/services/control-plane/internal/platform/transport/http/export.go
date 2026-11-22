package transport

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"sort"
	"time"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
)

const exportZIPLimit = 48 << 20
const exportTimeout = 30 * time.Second

func (s *Server) export(w http.ResponseWriter, r *http.Request, access experiment.Access, run string) {
	// Production has one Server instance. This is transient process protection,
	// independent of organization quotas or billing.
	if !s.exportBusy.CompareAndSwap(false, true) {
		failure(w, experiment.Failure{Status: 503, Code: "EXPORT_BUSY"})
		return
	}
	defer s.exportBusy.Store(false)
	ctx, cancel := context.WithTimeout(r.Context(), exportTimeout)
	defer cancel()
	files, err := s.Experiments.Export(ctx, access, run)
	if err != nil {
		failure(w, err)
		return
	}
	archive, err := buildExportZIP(ctx, files, exportZIPLimit)
	if err != nil {
		failure(w, err)
		return
	}
	authorize := func(ctx context.Context) error { return s.Experiments.AuthorizeExport(ctx, access, run) }
	defer func() {
		if p := recover(); p != nil {
			if p == http.ErrAbortHandler {
				s.metrics.abortedExports.Add(1)
			}
			panic(p)
		}
	}()
	if err = deliverExport(ctx, w, archive, run, authorize); err != nil {
		failure(w, err)
	}
}

type zipBudget struct {
	data  []byte
	ctx   context.Context
	limit int
}

func (b *zipBudget) Write(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
	}
	if len(p) > b.limit-len(b.data) {
		return 0, experiment.Failure{Status: 409, Code: "EXPORT_LIMIT_EXCEEDED"}
	}
	needed := len(b.data) + len(p)
	if needed > cap(b.data) {
		capacity := min(b.limit, max(needed, 2*cap(b.data)))
		next := make([]byte, len(b.data), capacity)
		copy(next, b.data)
		b.data = next
	}
	b.data = append(b.data, p...)
	return len(p), nil
}

func buildExportZIP(ctx context.Context, files map[string][]byte, limit int) ([]byte, error) {
	buffer := zipBudget{ctx: ctx, limit: limit}
	archive := zip.NewWriter(&buffer)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry, err := archive.Create(name)
		if err != nil {
			return nil, err
		}
		// Bound individual CPU work between context checks even for compressible data.
		data := files[name]
		for len(data) > 0 {
			if err = ctx.Err(); err != nil {
				return nil, experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
			}
			n := min(len(data), exportChunkSize)
			written, err := entry.Write(data[:n])
			if err != nil {
				return nil, err
			}
			if written != n {
				return nil, io.ErrShortWrite
			}
			data = data[n:]
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.data, nil
}
