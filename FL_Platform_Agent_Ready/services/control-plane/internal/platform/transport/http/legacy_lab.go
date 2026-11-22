package transport

import (
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/legacylab"
)

func (s *Server) legacyLab(w http.ResponseWriter, r *http.Request, principal identity.Principal, parts []string) {
	if r.URL.Path != "/api/v1/"+strings.Join(parts, "/") {
		failure(w, legacylab.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	self := len(parts) == 2 && parts[0] == "me" && parts[1] == "legacy-lab"
	admin := len(parts) == 4 && parts[0] == "admin" && parts[1] == "users" && parts[3] == "legacy-lab"
	if (!self && !admin) || (self && r.Method != http.MethodGet) || (admin && r.Method != http.MethodGet && r.Method != http.MethodPut) {
		failure(w, legacylab.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	if r.URL.RawQuery != "" {
		failure(w, legacylab.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	service := legacylab.Service{Pool: s.Identity.Pool}
	var result legacylab.Admission
	var err error
	if self {
		result, err = service.Self(r.Context(), principal)
	} else {
		id, parseErr := uuid.Parse(parts[2])
		if parseErr != nil || id == uuid.Nil {
			failure(w, legacylab.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		if r.Method == http.MethodGet {
			result, err = service.Get(r.Context(), principal, id.String())
		} else {
			var in legacylab.Change
			in, err = legacyLabChange(w, r)
			if err == nil {
				result, err = service.Change(r.Context(), principal, id.String(), in)
			}
		}
	}
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, result)
}

// Public input bounds are stricter than the internal domain's Go-int range.
// A revision pointer distinguishes missing/null from a valid create revision0.
func legacyLabChange(w http.ResponseWriter, r *http.Request) (legacylab.Change, error) {
	var result legacylab.Change
	raw, err := body(w, r)
	if err != nil {
		return result, err
	}
	raw, err = experiment.Canonical(raw)
	if err != nil {
		return result, err
	}
	var in struct {
		Status   string `json:"status"`
		Revision *int64 `json:"revision"`
		Reason   string `json:"reason"`
	}
	if err = experiment.Decode(raw, &in); err != nil {
		return result, err
	}
	if in.Revision == nil || *in.Revision < 0 || *in.Revision > 2147483647 || (in.Status != "active" && in.Status != "revoked") ||
		!utf8.ValidString(in.Reason) || utf8.RuneCountInString(in.Reason) > 240 || strings.IndexFunc(in.Reason, unicode.IsControl) >= 0 || strings.TrimSpace(in.Reason) == "" {
		return result, legacylab.Failure{Status: 400, Code: "INVALID_REQUEST"}
	}
	return legacylab.Change{Status: in.Status, Revision: int(*in.Revision), Reason: strings.TrimSpace(in.Reason)}, nil
}
