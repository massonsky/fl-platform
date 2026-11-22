package transport

import (
	"net/http"

	"github.com/google/uuid"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func projectID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"}
	}
	return id.String(), nil
}
func (s *Server) organizationProjects(w http.ResponseWriter, r *http.Request, user identity.User, parts []string) {
	if s.ProjectAccess == nil {
		failure(w, projectaccess.Failure{Status: 503, Code: "ORG_PROJECTS_NOT_CONFIGURED"})
		return
	}
	if len(s.CursorKey) == 0 {
		failure(w, projectaccess.Failure{Status: 503, Code: "DEPENDENCY_UNAVAILABLE"})
		return
	}
	if len(parts) < 3 || len(parts) > 6 || parts[0] != "organizations" || parts[2] != "projects" {
		failure(w, projectaccess.Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	org, err := projectID(parts[1])
	if err != nil {
		failure(w, err)
		return
	}
	project := ""
	if len(parts) >= 4 {
		project, err = projectID(parts[3])
		if err != nil {
			failure(w, err)
			return
		}
	}
	switch {
	case len(parts) == 3 && r.Method == http.MethodGet:
		s.organizationProjectPage(w, r, user, org, "", "projects")
	case len(parts) == 4 && r.Method == http.MethodGet:
		if r.URL.RawQuery != "" {
			failure(w, projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"})
			return
		}
		item, err := s.ProjectAccess.Get(r.Context(), user.Principal(), org, project)
		if err != nil {
			failure(w, err)
		} else {
			write(w, 200, item)
		}
	case len(parts) == 5 && parts[4] == "access" && r.Method == http.MethodGet:
		s.organizationProjectPage(w, r, user, org, project, "access")
	case len(parts) == 6 && parts[4] == "access" && parts[5] == "candidates" && r.Method == http.MethodGet:
		s.organizationProjectPage(w, r, user, org, project, "candidates")
	case len(parts) == 4 && r.Method == http.MethodPut:
		s.organizationProjectCreate(w, r, user, org, project)
	case len(parts) == 5 && parts[4] == "visibility" && r.Method == http.MethodPatch:
		s.organizationProjectVisibility(w, r, user, org, project)
	case len(parts) == 6 && parts[4] == "members" && r.Method == http.MethodPut:
		target, err := projectID(parts[5])
		if err != nil {
			failure(w, err)
			return
		}
		s.organizationProjectGrant(w, r, user, org, project, target)
	default:
		failure(w, projectaccess.Failure{Status: 404, Code: "NOT_FOUND"})
	}
}
func projectWriteBody(w http.ResponseWriter, r *http.Request, target any) error {
	if r.URL.RawQuery != "" {
		return projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"}
	}
	return organizationBody(w, r, target)
}
func (s *Server) organizationProjectCreate(w http.ResponseWriter, r *http.Request, user identity.User, org, project string) {
	var in struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := projectWriteBody(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	item, err := s.ProjectAccess.Create(r.Context(), user.Principal(), org, project, projectaccess.CreateInput{Slug: in.Slug, Name: in.Name})
	if err != nil {
		failure(w, err)
	} else {
		write(w, 200, item)
	}
}
func (s *Server) organizationProjectVisibility(w http.ResponseWriter, r *http.Request, user identity.User, org, project string) {
	var in struct {
		Visibility string `json:"visibility"`
		Revision   *int   `json:"acl_revision"`
	}
	if err := projectWriteBody(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	if in.Revision == nil {
		failure(w, projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	item, err := s.ProjectAccess.SetVisibility(r.Context(), user.Principal(), org, project, projectaccess.VisibilityInput{Visibility: in.Visibility, ACLRevision: *in.Revision})
	if err != nil {
		failure(w, err)
	} else {
		write(w, 200, item)
	}
}
func (s *Server) organizationProjectGrant(w http.ResponseWriter, r *http.Request, user identity.User, org, project, target string) {
	var in struct {
		Role     string `json:"role"`
		Status   string `json:"status"`
		Revision *int   `json:"revision"`
	}
	if err := projectWriteBody(w, r, &in); err != nil {
		failure(w, err)
		return
	}
	if in.Revision == nil {
		failure(w, projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	item, err := s.ProjectAccess.SaveGrant(r.Context(), user.Principal(), org, project, target, projectaccess.GrantInput{Role: in.Role, Status: in.Status, Revision: *in.Revision})
	if err != nil {
		failure(w, err)
	} else {
		write(w, 200, item)
	}
}
