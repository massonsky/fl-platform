package transport

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

func (s *Server) inference(w http.ResponseWriter, r *http.Request, user identity.User, path string) {
	if user.Role != "admin" && user.Role != "researcher" {
		failure(w, inference.Failure{Status: 403, Code: "FORBIDDEN"})
		return
	}
	if s.Inference == nil {
		failure(w, inference.Failure{Status: 503, Code: "AI_NOT_CONFIGURED"})
		return
	}
	if path == "ai/assistants" || strings.HasPrefix(path, "ai/assistants/") {
		s.assistants(w, r, user.ID, path)
		return
	}
	if path == "ai/conversations" || strings.HasPrefix(path, "ai/conversations/") {
		s.chat(w, r, user, path)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) == 2 && parts[1] == "providers" && r.Method == "GET" {
		items, err := s.Inference.List(r.Context(), user.ID)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items, "allowed_base_urls": s.Inference.Policy.Allowed})
		}
		return
	}
	if len(parts) >= 3 && parts[1] == "providers" {
		id, err := uuid.Parse(parts[2])
		if err != nil {
			failure(w, inference.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		if len(parts) == 3 && r.Method == "PUT" {
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
			var in inference.Input
			if err = experiment.Decode(raw, &in); err != nil {
				failure(w, err)
				return
			}
			out, err := s.Inference.Save(r.Context(), user.ID, id.String(), in)
			if err != nil {
				failure(w, err)
			} else {
				status := 200
				if in.Revision == 0 {
					status = 201
				}
				write(w, status, out)
			}
			return
		}
		if len(parts) == 4 && parts[3] == "models" && r.Method == "POST" {
			raw, err := body(w, r)
			if err != nil {
				failure(w, err)
				return
			}
			if len(raw) > 0 && string(raw) != "{}" {
				failure(w, inference.Failure{Status: 400, Code: "INVALID_REQUEST"})
				return
			}
			items, err := s.Inference.Models(r.Context(), user.ID, id.String())
			if err != nil {
				failure(w, err)
			} else {
				write(w, 200, map[string]any{"items": items})
			}
			return
		}
	}
	failure(w, inference.Failure{Status: 404, Code: "NOT_FOUND"})
}
