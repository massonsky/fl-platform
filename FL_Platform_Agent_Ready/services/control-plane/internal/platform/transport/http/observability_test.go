package transport

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthCorrelationAndMetrics(t *testing.T) {
	s := &Server{}
	result := httptest.NewRecorder()
	s.ServeHTTP(result, httptest.NewRequest("GET", "/healthz?password=do-not-log", nil))
	if result.Code != 200 || result.Header().Get("X-Request-ID") == "" {
		t.Fatal("missing health or correlation")
	}
	if s.metrics.classes[2].Load() != 1 {
		t.Fatal("success counter missing")
	}
	metrics := httptest.NewRecorder()
	s.prometheus(metrics)
	if !strings.Contains(metrics.Body.String(), `status_class="2xx"} 1`) {
		t.Fatal(metrics.Body.String())
	}
	if strings.Contains(metrics.Body.String(), "password") {
		t.Fatal("private input in metrics")
	}
}
