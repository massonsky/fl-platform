package transport

import (
	"encoding/hex"
	"encoding/json"
	"github.com/google/uuid"
	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *Server) administration(w http.ResponseWriter, r *http.Request, user identity.User, path string) {
	store := admin.Service{Pool: s.Identity.Pool, Limits: s.Identity.Limits, Work: s.Identity.Work}
	parts := strings.Split(path, "/")
	if r.Method == "GET" && len(parts) == 2 && parts[0] == "admin" {
		s.administrationPage(w, r, user, store, parts[1])
		return
	}
	op, target := "", ""
	if r.Method == "POST" {
		if path == "account/password" {
			op = "password.change"
		}
		if path == "admin/users" {
			op = "user.create"
		}
		if len(parts) == 3 && parts[0] == "admin" && (parts[1] == "users" || parts[1] == "agents") {
			if _, err := uuid.Parse(parts[2]); err != nil {
				failure(w, admin.Failure{Status: 400, Code: "INVALID_REQUEST"})
				return
			}
			target = parts[2]
			if parts[1] == "users" {
				op = "user.update"
			} else {
				op = "agent.update"
			}
		}
	}
	if op == "" {
		failure(w, admin.Failure{Status: 404, Code: "NOT_FOUND"})
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
	var in admin.Input
	if err = experiment.Decode(raw, &in); err != nil {
		failure(w, err)
		return
	}
	// A keyed digest prevents receipts from becoming offline password oracles.
	fingerprint := hex.EncodeToString(s.sign(append([]byte(op+"/"+target+"\n"), raw...)))
	var result json.RawMessage
	if op == "user.create" || op == "password.change" || op == "user.update" && in.Password != "" {
		client, clientErr := credentialClient(r)
		if clientErr != nil {
			failure(w, clientErr)
			return
		}
		result, err = store.MutateCredential(r.Context(), user.Principal(), admin.CredentialMutation{
			Client: client, Operation: op, Target: target, Key: r.Header.Get("Idempotency-Key"), Fingerprint: fingerprint, Input: in,
		})
	} else {
		result, err = store.Mutate(r.Context(), user.Principal(), op, target, r.Header.Get("Idempotency-Key"), fingerprint, in)
	}
	if err != nil {
		failure(w, err)
		return
	}
	status := 200
	if op == "user.create" {
		status = 201
	}
	write(w, status, result)
}
func (s *Server) administrationPage(w http.ResponseWriter, r *http.Request, user identity.User, store admin.Service, kind string) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			failure(w, admin.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		limit = n
	}
	scope := user.ID + "/admin/" + kind
	c := cursor{Scope: scope, Cut: time.Now().UTC(), Expires: time.Now().Add(15 * time.Minute).Unix()}
	if v := r.URL.Query().Get("cursor"); v != "" {
		var err error
		c, err = s.decodeCursor(v, scope)
		if err != nil {
			failure(w, err)
			return
		}
	}
	items, more, err := store.Page(r.Context(), user.Principal(), kind, c.Offset, limit)
	if err != nil {
		failure(w, err)
		return
	}
	var next any
	if more {
		c.Offset += limit
		next = s.encodeCursor(c)
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
