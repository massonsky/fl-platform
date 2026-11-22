package transport

import (
	"net/http"
	"net/url"
	"time"

	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

func (s *Server) organizationProjectPage(w http.ResponseWriter, r *http.Request, user identity.User, org, project, route string) {
	projects := route == "projects"
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		failure(w, projectaccess.Failure{Status: 400, Code: "INVALID_REQUEST"})
		return
	}
	limit, c, err := s.projectPageQuery(query, projectScope(user.ID, org, project, route), projects)
	if err != nil {
		failure(w, err)
		return
	}
	if projects {
		in := projectaccess.PageInput{Limit: limit, Cut: c.Cut}
		if c.AfterAt != nil {
			in.After = &projectaccess.ProjectKey{CreatedAt: *c.AfterAt, ID: c.AfterID}
		}
		page, err := s.ProjectAccess.Page(r.Context(), user.Principal(), org, in)
		if err != nil {
			failure(w, err)
			return
		}
		var next any
		if page.Next != nil {
			c.Cut, c.AfterAt, c.AfterID = page.Cut, &page.Next.CreatedAt, page.Next.ID
			if c.Expires == 0 {
				c.Expires = c.Cut.Add(15 * time.Minute).Unix()
			}
			next = s.encodeProjectCursor(c)
		}
		write(w, 200, map[string]any{"items": page.Items, "context": page.Context, "next_cursor": next})
		return
	}
	in := projectaccess.MemberPageInput{Limit: limit, Cut: c.Cut, AfterID: c.AfterID}
	var page projectaccess.AccessPage
	if route == "candidates" {
		page, err = s.ProjectAccess.Candidates(r.Context(), user.Principal(), org, project, in)
	} else {
		page, err = s.ProjectAccess.Roster(r.Context(), user.Principal(), org, project, in)
	}
	if err != nil {
		failure(w, err)
		return
	}
	var next any
	if page.NextUserID != "" {
		c.Cut, c.AfterID = page.Cut, page.NextUserID
		if c.Expires == 0 {
			c.Expires = c.Cut.Add(15 * time.Minute).Unix()
		}
		next = s.encodeProjectCursor(c)
	}
	write(w, 200, map[string]any{"project": page.Project, "items": page.Items, "next_cursor": next})
}
