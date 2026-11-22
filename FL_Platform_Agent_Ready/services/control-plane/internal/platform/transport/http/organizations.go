package transport

import (
	"net/http"
	"strings"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
)

func (s *Server) organizations(w http.ResponseWriter, r *http.Request, principal identity.Principal, path string) {
	if s.Organizations == nil {
		failure(w, organization.Failure{Status: 503, Code: "ORGS_NOT_CONFIGURED"})
		return
	}
	ctx := r.Context() // Service applies the bounded context to every SQL callback.
	parts := strings.Split(path, "/")
	if len(parts) == 1 && r.Method == http.MethodGet {
		items, err := s.Organizations.List(ctx, principal)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items})
		}
		return
	}
	if len(parts) < 2 || len(parts) > 4 || (len(parts) > 2 && parts[2] != "members") {
		failure(w, organization.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	id, err := uuid.Parse(parts[1])
	if err != nil || id == uuid.Nil {
		failure(w, organization.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	switch {
	case len(parts) == 2 && r.Method == http.MethodPut:
		var input struct {
			Name string `json:"name"`
		}
		if err := organizationBody(w, r, &input); err != nil {
			failure(w, err)
			return
		}
		item, err := s.Organizations.Create(ctx, principal, id.String(), input.Name)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, item)
		}
	case len(parts) == 3 && r.Method == http.MethodGet:
		items, err := s.Organizations.ListMembers(ctx, principal, id.String())
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, map[string]any{"items": items})
		}
	case len(parts) == 4 && r.Method == http.MethodPatch:
		userID, err := uuid.Parse(parts[3])
		if err != nil || userID == uuid.Nil {
			failure(w, organization.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		var input organization.MemberChange
		if err := organizationBody(w, r, &input); err != nil {
			failure(w, err)
			return
		}
		if input.Revision < 1 || uint64(input.Revision) > 9007199254740991 {
			failure(w, organization.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		item, err := s.Organizations.ChangeMember(ctx, principal, id.String(), userID.String(), input)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, item)
		}
	default:
		failure(w, organization.Failure{Status: 404, Code: "NOT_FOUND"})
	}
}

func organizationBody(w http.ResponseWriter, r *http.Request, target any) error {
	raw, err := body(w, r)
	if err != nil {
		return err
	}
	raw, err = experiment.Canonical(raw)
	if err != nil {
		return err
	}
	return experiment.Decode(raw, target)
}
