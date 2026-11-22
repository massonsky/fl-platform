package transport

import (
	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/assistant"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
	"net/http"
	"strings"
)

func (s *Server) assistants(w http.ResponseWriter, r *http.Request, owner, path string) {
	store := assistant.Store{Pool: s.Identity.Pool}
	if path == "ai/assistants" && r.Method == "GET" {
		items, err := store.List(r.Context(), owner)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items})
		}
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 3 || r.Method != "PUT" {
		failure(w, inference.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	id, err := uuid.Parse(parts[2])
	if err != nil {
		failure(w, inference.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	raw, err := body(w, r)
	if err != nil {
		failure(w, err)
		return
	}
	raw, err = experiment.Canonical(raw)
	if err != nil {
		failure(w, err)
		return
	}
	var in assistant.Input
	if err = experiment.Decode(raw, &in); err != nil {
		failure(w, err)
		return
	}
	p, err := store.Save(r.Context(), owner, id.String(), in)
	if err != nil {
		failure(w, err)
		return
	}
	status := 200
	if in.Revision == 0 {
		status = 201
	}
	write(w, status, p)
}
