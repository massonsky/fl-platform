package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func identityFailure(err error) error {
	if errors.Is(err, identity.ErrUnauthorized) {
		return experiment.Failure{Status: 401, Code: "UNAUTHORIZED"}
	}
	return experiment.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"}
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	blobs, ok := s.Experiments.Blobs.(interface{ Ready(context.Context) error })
	if s.Experiments.Pool == nil || !ok ||
		s.Experiments.Pool.Ping(ctx) != nil || blobs.Ready(ctx) != nil {
		write(w, 503, map[string]string{"status": "not_ready"})
		return
	}
	write(w, 200, map[string]string{"status": "ok"})
}
