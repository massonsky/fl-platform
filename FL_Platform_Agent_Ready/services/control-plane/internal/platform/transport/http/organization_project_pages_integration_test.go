//go:build integration

package transport

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

type projectHTTPPage struct {
	Items   []projectaccess.Project   `json:"items"`
	Next    *string                   `json:"next_cursor"`
	Context projectaccess.PageContext `json:"context"`
}

func readProjectHTTPPage(t *testing.T, w *httptest.ResponseRecorder) projectHTTPPage {
	t.Helper()
	var page projectHTTPPage
	if json.Unmarshal(w.Body.Bytes(), &page) != nil || page.Items == nil {
		t.Fatal("bad project page", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "SessionHash") || strings.Contains(w.Body.String(), `"after_id"`) || strings.Contains(w.Body.String(), `"Cut"`) {
		t.Fatal("internal cursor/principal leaked", w.Body.String())
	}
	return page
}
func TestOrganizationProjectHTTPCursorsBindEveryScopeAndRecheckMembership(t *testing.T) {
	f := projectHTTPDB(t)
	id := f.create(t, "first")
	f.create(t, "second")
	f.create(t, "third")
	authorizationExec(t, f.pool, "UPDATE projects SET created_at='2026-01-01T00:00:00Z' WHERE organization_id=$1", f.org)
	first := readProjectHTTPPage(t, f.call(t, "owner", "GET", f.base()+"?limit=1", "", 200, ""))
	if len(first.Items) != 1 || first.Next == nil || !first.Context.Capabilities.CreateProjects {
		t.Fatal(first)
	}
	next := url.QueryEscape(*first.Next)
	second := readProjectHTTPPage(t, f.call(t, "owner", "GET", f.base()+"?limit=100&cursor="+next, "", 200, ""))
	if len(second.Items) != 2 || second.Next != nil || second.Items[0].ID == first.Items[0].ID {
		t.Fatal("bad continuation", second)
	}
	// Actor belongs to both organizations; the cursor still cannot cross context.
	authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'admin')", f.otherOrg, f.users["owner"].ID)
	f.call(t, "owner", "GET", "organizations/"+f.otherOrg+"/projects?cursor="+next, "", 400, "INVALID_CURSOR")
	f.call(t, "other", "GET", f.base()+"?cursor="+next, "", 400, "INVALID_CURSOR")
	f.call(t, "owner", "GET", f.base()+"/"+id+"/access?cursor="+next, "", 400, "INVALID_CURSOR")
	scope := projectScope(f.users["owner"].ID, f.org, "", "projects")
	c, err := f.s.decodeProjectCursor(*first.Next, scope, true)
	if err != nil {
		t.Fatal(err)
	}
	c.Expires = time.Now().Add(-time.Second).Unix()
	f.call(t, "owner", "GET", f.base()+"?cursor="+url.QueryEscape(f.s.encodeProjectCursor(c)), "", 400, "INVALID_CURSOR")
	tampered := (*first.Next)[:len(*first.Next)-4] + "AAAA"
	f.call(t, "owner", "GET", f.base()+"?cursor="+url.QueryEscape(tampered), "", 400, "INVALID_CURSOR")
	authorizationExec(t, f.pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2", f.org, f.users["owner"].ID)
	f.call(t, "owner", "GET", f.base()+"?cursor="+next, "", 404, "NOT_FOUND")
	f.call(t, "owner", "GET", f.base()+"/"+id, "", 404, "NOT_FOUND")
}

func TestOrganizationProjectHTTPRosterPagesAndStalePrincipal(t *testing.T) {
	f := projectHTTPDB(t)
	id := f.create(t, "roster")
	otherProject := f.create(t, "other-roster")
	path := f.base() + "/" + id
	var first struct {
		Items   []projectaccess.AccessMember `json:"items"`
		Next    *string                      `json:"next_cursor"`
		Project projectaccess.Project        `json:"project"`
	}
	w := f.call(t, "owner", "GET", path+"/access?limit=1", "", 200, "")
	if json.Unmarshal(w.Body.Bytes(), &first) != nil || len(first.Items) != 1 || first.Next == nil || !first.Project.Capabilities.ManageAccess {
		t.Fatal(w.Body.String())
	}
	query := "?cursor=" + url.QueryEscape(*first.Next)
	f.call(t, "owner", "GET", path+"/access/candidates"+query, "", 400, "INVALID_CURSOR")
	f.call(t, "owner", "GET", f.base()+"/"+otherProject+"/access"+query, "", 400, "INVALID_CURSOR")
	f.call(t, "owner", "GET", path+"/access"+query, "", 200, "")
	var candidates struct {
		Items []projectaccess.AccessMember `json:"items"`
		Next  *string                      `json:"next_cursor"`
	}
	w = f.call(t, "owner", "GET", path+"/access/candidates?limit=1", "", 200, "")
	if json.Unmarshal(w.Body.Bytes(), &candidates) != nil || len(candidates.Items) != 1 || candidates.Next == nil {
		t.Fatal(w.Body.String())
	}
	if !candidates.Items[0].CanGrant || candidates.Items[0].OrganizationRole == "owner" {
		t.Fatal(candidates.Items)
	}
	f.call(t, "owner", "GET", path+"/access/candidates?cursor="+url.QueryEscape(*candidates.Next), "", 200, "")
	actor := f.users["owner"]
	actor.SessionHash = identity.Hash(actor.ID)
	authorizationExec(t, f.pool, "DELETE FROM sessions WHERE user_id=$1", actor.ID)
	r := httptest.NewRequest("PUT", "/api/v1/"+path, strings.NewReader(`{"slug":"roster","name":"Project"}`))
	r.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	f.s.organizationProjects(w, r, actor, strings.Split(path, "/"))
	if w.Code != 401 || !strings.Contains(w.Body.String(), "UNAUTHORIZED") {
		t.Fatal("stale authentication replay", w.Code, w.Body.String())
	}
	f.call(t, "owner", "GET", path+"/access"+query, "", 401, "UNAUTHORIZED")
}
