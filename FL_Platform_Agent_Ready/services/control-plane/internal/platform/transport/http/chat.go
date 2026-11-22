package transport

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/conversation"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

func (s *Server) chat(w http.ResponseWriter, r *http.Request, user identity.User, path string) {
	store := conversation.Store{Pool: s.Identity.Pool}
	parts := strings.Split(path, "/")
	id := ""
	if len(parts) > 2 {
		parsed, err := uuid.Parse(parts[2])
		if err != nil {
			failure(w, inference.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		id = parsed.String()
	}
	if len(parts) == 2 && r.Method == "GET" {
		items, err := store.List(r.Context(), user.ID)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items})
		}
		return
	}
	if len(parts) == 3 {
		switch r.Method {
		case "GET":
			c, items, err := store.Detail(r.Context(), user.ID, id)
			if err != nil {
				failure(w, err)
			} else {
				write(w, 200, map[string]any{"conversation": c, "items": items})
			}
			return
		case "DELETE":
			if err := store.Cancel(r.Context(), user.ID, id, true); err != nil {
				failure(w, err)
			} else {
				write(w, 200, map[string]bool{"deleted": true})
			}
			return
		}
	}
	if r.Method != "POST" {
		failure(w, inference.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	raw, err := body(w, r)
	if err != nil {
		failure(w, err)
		return
	}
	if len(parts) == 4 && parts[3] == "cancel" {
		if len(raw) > 0 && string(raw) != "{}" {
			failure(w, inference.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		if err = store.Cancel(r.Context(), user.ID, id, false); err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]bool{"cancelled": true})
		}
		return
	}
	raw, err = experiment.Canonical(raw)
	if err != nil {
		failure(w, err)
		return
	}
	if len(parts) == 2 {
		var in struct {
			Provider          string `json:"provider_id"`
			Model             string `json:"model"`
			Consent           bool   `json:"consent"`
			AssistantID       string `json:"assistant_id"`
			AssistantRevision int    `json:"assistant_revision"`
		}
		if err = experiment.Decode(raw, &in); err != nil {
			failure(w, err)
			return
		}
		c, err := store.CreateWithAssistant(r.Context(), user.ID, in.Provider, in.Model, in.Consent, in.AssistantID, in.AssistantRevision)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 201, c)
		}
		return
	}
	if len(parts) == 4 && parts[3] == "messages" {
		var in struct {
			ID      string `json:"request_id"`
			Content string `json:"content"`
		}
		if err = experiment.Decode(raw, &in); err != nil {
			failure(w, err)
			return
		}
		digest := hex.EncodeToString(s.sign(append([]byte("chat/"+id+"\n"), raw...)))
		job, err := store.Begin(r.Context(), user.ID, id, in.ID, in.Content, digest)
		if err != nil {
			failure(w, err)
			return
		}
		if job.Replay {
			write(w, 200, job.Generation)
			return
		}
		s.streamChat(w, r, user.ID, store, job, s.Inference.Chat)
		return
	}
	failure(w, inference.Failure{Status: 404, Code: "NOT_FOUND"})
}
