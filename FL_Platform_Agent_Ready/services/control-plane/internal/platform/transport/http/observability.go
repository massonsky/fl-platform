package transport

import (
	"fmt"
	"github.com/google/uuid"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

type requestMetrics struct {
	classes        [6]atomic.Uint64
	nanoseconds    atomic.Uint64
	abortedExports atomic.Uint64
}
type observedWriter struct {
	http.ResponseWriter
	status int
}

func (w *observedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *observedWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *observedWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID := uuid.NewString()
	w.Header().Set("X-Request-ID", requestID)
	observed := &observedWriter{ResponseWriter: w}
	defer func() {
		p := recover()
		s.recordRequest(observed, r, started, requestID, p)
		if p != nil {
			panic(p)
		} // Preserve ErrAbortHandler for net/http.
	}()
	s.serveHTTP(observed, r)
}
func (s *Server) recordRequest(observed *observedWriter, r *http.Request, started time.Time, requestID string, p any) {
	status := observed.status
	if status == 0 {
		status = 200
		if p != nil {
			status = 500
		}
	}
	outcome := "completed"
	if p == http.ErrAbortHandler {
		outcome = "aborted"
	} else if p != nil {
		outcome = "panic"
	}
	elapsed := time.Since(started)
	if class := status / 100; class >= 1 && class <= 5 {
		s.metrics.classes[class].Add(1)
	}
	s.metrics.nanoseconds.Add(uint64(elapsed.Nanoseconds()))
	// Never log paths, query strings, request bodies or authorization headers.
	slog.Info("http_request", "request_id", requestID, "method", r.Method, "status", status, "outcome", outcome, "duration_ms", elapsed.Milliseconds())
}
func (s *Server) prometheus(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP fl_http_requests_total HTTP requests handled by this process.")
	fmt.Fprintln(w, "# TYPE fl_http_requests_total counter")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(w, "fl_http_requests_total{status_class=\"%dxx\"} %d\n", i, s.metrics.classes[i].Load())
	}
	fmt.Fprintln(w, "# HELP fl_http_duration_seconds_sum Cumulative HTTP handler duration.")
	fmt.Fprintln(w, "# TYPE fl_http_duration_seconds_sum counter")
	fmt.Fprintf(w, "fl_http_duration_seconds_sum %.6f\n", float64(s.metrics.nanoseconds.Load())/1e9)
	fmt.Fprintln(w, "# HELP fl_export_aborted_total Export responses aborted after headers; accepted bytes cannot be recalled.")
	fmt.Fprintln(w, "# TYPE fl_export_aborted_total counter")
	fmt.Fprintf(w, "fl_export_aborted_total %d\n", s.metrics.abortedExports.Load())
}
