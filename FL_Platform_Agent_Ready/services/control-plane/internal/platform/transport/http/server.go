package transport

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type Server struct {
	ProjectAccess *projectaccess.Service
	Organizations *organization.Service
	Inference     *inference.Store
	Identity      identity.Store
	Experiments   experiment.Store
	CursorKey     []byte
	metrics       requestMetrics
	exportBusy    atomic.Bool
}

func write(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func failure(w http.ResponseWriter, err error) {
	status, code := 500, "INTERNAL"
	var ai inference.Failure
	var api experiment.Failure
	var management admin.Failure
	var org organization.Failure
	var project projectaccess.Failure
	var lab legacylab.Failure
	var credential credentials.Failure
	var pg *pgconn.PgError
	if errors.As(err, &ai) {
		status, code = ai.Status, ai.Code
	} else if errors.As(err, &api) {
		status, code = api.Status, api.Code
	} else if errors.As(err, &management) {
		status, code = management.Status, management.Code
	} else if errors.As(err, &org) {
		status, code = org.Status, org.Code
	} else if errors.As(err, &project) {
		status, code = project.Status, project.Code
	} else if errors.As(err, &lab) {
		status, code = lab.Status, lab.Code
	} else if errors.As(err, &credential) {
		status, code = credential.Status, credential.Code
	} else if errors.As(err, &pg) && pg.Code == "23505" {
		status, code = 409, "IDEMPOTENCY_CONFLICT"
		if pg.ConstraintName == "projects_slug_key" {
			code = "SLUG_CONFLICT"
		}
		if pg.ConstraintName == "users_external_subject_key" {
			code = "USERNAME_CONFLICT"
		}
	}
	requestID := w.Header().Get("X-Request-ID")
	if requestID == "" {
		requestID = uuid.NewString()
	}
	w.Header().Set("X-Request-ID", requestID)
	if status == 401 {
		w.Header().Set("WWW-Authenticate", "Bearer")
	}
	if status == 429 {
		w.Header().Set("Retry-After", "60")
		if credential.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(credential.RetryAfter))
		}
	}
	if status == 503 {
		w.Header().Set("Retry-After", "3")
	}
	write(w, status, map[string]any{"code": code, "message": code, "request_id": requestID, "retryable": status >= 500 || status == 429})
}
func body(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, experiment.Failure{Status: 413, Code: "PAYLOAD_TOO_LARGE"}
	}
	if len(data) > 0 {
		media, _, e := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if e != nil || media != "application/json" {
			return nil, experiment.Failure{Status: 415, Code: "UNSUPPORTED_MEDIA_TYPE"}
		}
	}
	return data, nil
}
func (s *Server) authenticated(r *http.Request) (identity.User, error) {
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Bearer ") {
		return identity.User{}, experiment.Failure{Status: 401, Code: "UNAUTHORIZED"}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	user, err := s.Identity.Authenticate(ctx, strings.TrimPrefix(value, "Bearer "))
	if err != nil {
		return user, identityFailure(err)
	}
	return user, nil
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.Path == "/healthz" {
		write(w, 200, map[string]string{"status": "ok"})
		return
	}
	if r.URL.Path == "/readyz" {
		s.ready(w, r)
		return
	}
	if r.URL.Path == "/api/v1/session" && r.Method == "POST" {
		s.login(w, r)
		return
	}
	user, err := s.authenticated(r)
	if err != nil {
		failure(w, err)
		return
	}
	if r.URL.Path == "/metrics" && r.Method == "GET" {
		if user.Role != "admin" {
			failure(w, experiment.Failure{Status: 403, Code: "FORBIDDEN"})
			return
		}
		s.prometheus(w)
		return
	}
	if r.URL.Path == "/api/v1/session" && r.Method == "DELETE" {
		if err := s.Identity.Logout(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")); err != nil {
			failure(w, identityFailure(err))
			return
		}
		write(w, 200, map[string]bool{"logged_out": true})
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/"), "/")
	parts := strings.Split(path, "/")
	if (len(parts) >= 2 && parts[0] == "me" && parts[1] == "legacy-lab") ||
		(len(parts) >= 4 && parts[0] == "admin" && parts[1] == "users" && parts[3] == "legacy-lab") {
		s.legacyLab(w, r, user.Principal(), parts)
		return
	}
	if len(parts) >= 3 && parts[0] == "organizations" && parts[2] == "projects" {
		s.organizationProjects(w, r, user, parts)
		return
	}
	if path == "organizations" || strings.HasPrefix(path, "organizations/") {
		s.organizations(w, r, user.Principal(), path)
		return
	}
	if strings.HasPrefix(path, "ai/") {
		s.inference(w, r, user, path)
		return
	}
	if strings.HasPrefix(path, "admin/") || path == "account/password" {
		s.administration(w, r, user, path)
		return
	}
	if len(parts) > 1 {
		if _, err := uuid.Parse(parts[1]); err != nil {
			failure(w, experiment.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
	}
	if r.Method == "POST" {
		s.mutate(w, r, user, parts)
		return
	}
	if r.Method != "GET" {
		failure(w, experiment.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	if path == "me" {
		write(w, 200, user)
		return
	}
	if path == "presets" {
		items, err := s.Experiments.Presets(r.Context())
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items})
		}
		return
	}
	if len(parts) == 2 && (parts[0] == "runs" || parts[0] == "experiments") {
		result, err := s.Experiments.Detail(r.Context(), experiment.Access{Principal: user.Principal()}, parts[0], parts[1])
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, result)
		}
		return
	}
	if len(parts) == 3 && parts[0] == "runs" && parts[2] == "export" {
		s.export(w, r, experiment.Access{Principal: user.Principal()}, parts[1])
		return
	}
	s.list(w, r, user, parts)
}
func (s *Server) mutate(w http.ResponseWriter, r *http.Request, user identity.User, parts []string) {
	operation, id := "", ""
	if len(parts) == 1 && parts[0] == "projects" {
		operation = "project"
	}
	if len(parts) == 3 {
		id = parts[1]
		switch {
		case parts[0] == "projects" && parts[2] == "experiments":
			operation = "experiment"
		case parts[0] == "experiments" && parts[2] == "runs":
			operation = "queue"
		case parts[0] == "runs" && parts[2] == "cancel":
			operation = "cancel"
		}
	}
	if operation == "" {
		failure(w, experiment.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	raw, err := body(w, r)
	if err != nil {
		failure(w, err)
		return
	}
	result, err := s.Experiments.Mutate(r.Context(), experiment.Access{Principal: user.Principal()}, operation, id, r.Header.Get("Idempotency-Key"), raw)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, result.Status, result.Body)
}
