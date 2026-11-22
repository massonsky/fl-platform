package transport

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type cursor struct {
	Scope   string    `json:"scope"`
	Cut     time.Time `json:"cut"`
	Offset  int       `json:"offset"`
	Expires int64     `json:"expires"`
}

func (s *Server) sign(data []byte) []byte {
	mac := hmac.New(sha256.New, s.CursorKey)
	mac.Write(data)
	return mac.Sum(nil)
}
func (s *Server) encodeCursor(c cursor) string {
	data, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(append(data, s.sign(data)...))
}
func (s *Server) decodeCursor(encoded, scope string) (cursor, error) {
	var c cursor
	bad := experiment.Failure{Status: 400, Code: "INVALID_CURSOR"}
	if len(encoded) > 2048 {
		return c, bad
	}
	packed, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(packed) <= sha256.Size {
		return c, bad
	}
	data, signature := packed[:len(packed)-sha256.Size], packed[len(packed)-sha256.Size:]
	if !hmac.Equal(signature, s.sign(data)) {
		return c, bad
	}
	if json.Unmarshal(data, &c) != nil || c.Scope != scope || c.Expires <= time.Now().Unix() || c.Offset < 0 {
		return c, bad
	}
	return c, nil
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, user identity.User, parts []string) {
	kind, id, filter := "", "", ""
	if len(parts) == 1 && parts[0] == "projects" {
		kind = "projects"
	}
	if len(parts) == 3 {
		if parts[0] == "projects" && (parts[2] == "experiments" || parts[2] == "runs") || parts[0] == "runs" && (parts[2] == "rounds" || parts[2] == "metrics" || parts[2] == "events") {
			kind, id = parts[2], parts[1]
		}
	}
	if kind == "" {
		failure(w, experiment.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	if kind == "runs" {
		filter = r.URL.Query().Get("status")
		if filter != "" && !strings.Contains("|draft|queued|running|completed|failed|cancelled|", "|"+filter+"|") {
			failure(w, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
	}
	if kind == "metrics" {
		filter = r.URL.Query().Get("split")
		if filter != "" && filter != "train_local" && filter != "validation_global" && filter != "test_global" {
			failure(w, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
	}
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		var err error
		limit, err = strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 100 {
			failure(w, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
	}
	scope := user.ID + "/" + kind + "/" + id + "/" + filter
	c := cursor{Scope: scope, Cut: time.Now().UTC(), Expires: time.Now().Add(15 * time.Minute).Unix()}
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		var err error
		c, err = s.decodeCursor(encoded, scope)
		if err != nil {
			failure(w, err)
			return
		}
	}
	items, err := s.Experiments.List(r.Context(), experiment.Access{Principal: user.Principal()}, kind, id, filter, c.Cut, c.Offset, limit+1)
	if err != nil {
		failure(w, err)
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		c.Offset += limit
		next = s.encodeCursor(c)
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
