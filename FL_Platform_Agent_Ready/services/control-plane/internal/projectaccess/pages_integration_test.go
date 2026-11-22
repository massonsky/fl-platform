//go:build integration

package projectaccess

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestServiceProjectPagesUseKeysetAndCurrentACL(t *testing.T) {
	pool, store, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	manager := servicePrincipal(t, pool, owner)
	member := accessMember(t, pool, org, "researcher", "researcher")
	principal := servicePrincipal(t, pool, member)
	ids := []string{project}
	for _, slug := range []string{"second", "third", "fourth"} {
		id := uuid.NewString()
		if _, err := s.Create(ctx, manager, org, id, CreateInput{slug, slug}); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	accessExec(t, pool, "UPDATE projects SET created_at='2026-01-01T00:00:00Z',visibility='organization' WHERE organization_id=$1", org)
	first, err := s.Page(ctx, principal, org, PageInput{Limit: 1})
	if err != nil || len(first.Items) != 1 || first.Items[0].ID != ids[0] || first.Next == nil || first.Context.Capabilities.CreateProjects {
		t.Fatal(first, err)
	}
	// Revoking an already traversed row must not make an offset skip the next row.
	if _, err = store.SaveGrant(ctx, owner, org, ids[0], member, GrantInput{"viewer", "revoked", 0}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Create(ctx, manager, org, uuid.NewString(), CreateInput{"after-cut", "After cut"}); err != nil {
		t.Fatal(err)
	}
	accessExec(t, pool, "UPDATE projects SET visibility='organization' WHERE slug='after-cut'")
	var collected []string
	input := PageInput{Limit: 1, Cut: first.Cut, After: first.Next}
	for {
		page, err := s.Page(ctx, principal, org, input)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Items {
			collected = append(collected, p.ID)
		}
		if page.Next == nil {
			break
		}
		input.After = page.Next
	}
	if !reflect.DeepEqual(collected, ids[1:]) {
		t.Fatal("keyset/cut skipped or added rows", collected, ids)
	}
	_, err = s.Get(ctx, principal, org, ids[0])
	accessFailure(t, err, 404, "NOT_FOUND")
	for _, input := range []PageInput{{Limit: 101}, {Limit: -1}, {Cut: time.Now().Add(time.Hour)}, {After: first.Next}} {
		_, err = s.Page(ctx, principal, org, input)
		accessFailure(t, err, 400, "INVALID_REQUEST")
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), "CreatedAt") || strings.Contains(string(encoded), "Next") || strings.Contains(string(encoded), "SessionHash") {
		t.Fatal("internal continuation serialized", string(encoded), err)
	}
	accessExec(t, pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2", org, member)
	_, err = s.Page(ctx, principal, org, input)
	accessFailure(t, err, 404, "NOT_FOUND")
}

func TestServiceEmptyPageCapabilitiesAndExactOrganization(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	p := servicePrincipal(t, pool, owner)
	empty := accessOrg(t, pool, owner)
	page, err := s.Page(ctx, p, empty, PageInput{})
	if err != nil || page.Items == nil || len(page.Items) != 0 || !page.Context.Capabilities.CreateProjects || page.Next != nil {
		t.Fatal(page, err)
	}
	_, err = s.Get(ctx, p, empty, project)
	accessFailure(t, err, 404, "NOT_FOUND")
	member := accessMember(t, pool, empty, "researcher", "researcher")
	memberPrincipal := servicePrincipal(t, pool, member)
	page, err = s.Page(ctx, memberPrincipal, empty, PageInput{})
	if err != nil || page.Context.Capabilities.CreateProjects {
		t.Fatal(page, err)
	}
	_, err = s.Page(ctx, memberPrincipal, org, PageInput{})
	accessFailure(t, err, 404, "NOT_FOUND")
	accessExec(t, pool, "UPDATE organizations SET state='suspended' WHERE id=$1", empty)
	page, err = s.Page(ctx, p, empty, PageInput{})
	if err != nil || page.Context.Capabilities.CreateProjects || page.Context.OrganizationState != "suspended" {
		t.Fatal(page, err)
	}
}

func TestServiceRosterAndCandidatesExplainCapsWithoutForeignMembers(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	ctx, s := context.Background(), Service{Pool: pool}
	p := servicePrincipal(t, pool, owner)
	viewer := accessMember(t, pool, org, "viewer", "researcher")
	orgViewer := accessMember(t, pool, org, "researcher", "viewer")
	inactive := accessMember(t, pool, org, "researcher", "researcher")
	accessExec(t, pool, "UPDATE organization_memberships SET status='revoked' WHERE organization_id=$1 AND user_id=$2", org, inactive)
	foreign := accessUser(t, pool, "admin")
	otherOrg := accessOrg(t, pool, foreign)
	if _, err := s.SaveGrant(ctx, p, org, project, viewer, GrantInput{"researcher", "active", 0}); err != nil {
		t.Fatal(err)
	}
	first, err := s.Roster(ctx, p, org, project, MemberPageInput{Limit: 2})
	if err != nil || len(first.Items) != 2 || first.NextUserID == "" {
		t.Fatal(first, err)
	}
	accessMember(t, pool, org, "researcher", "researcher") // excluded by the original cut
	second, err := s.Roster(ctx, p, org, project, MemberPageInput{Limit: 2, Cut: first.Cut, AfterID: first.NextUserID})
	if err != nil || len(second.Items) != 2 || second.NextUserID != "" {
		t.Fatal(second, err)
	}
	members := map[string]AccessMember{}
	for _, m := range append(first.Items, second.Items...) {
		members[m.UserID] = m
	}
	if len(members) != 4 || members[foreign].UserID != "" {
		t.Fatal("foreign/duplicate roster member", members)
	}
	if m := members[owner]; m.AccessSource != "organization_admin" || m.CanGrant || m.CanRevoke || m.GrantBlockedReason != "INHERITED_ACCESS" {
		t.Fatal(m)
	}
	if m := members[viewer]; m.Grant == nil || m.Grant.Revision != 1 || m.Capabilities.CreateExperiment || !m.Capabilities.Read || m.WriteCapReason != "GLOBAL_VIEWER" || !m.CanGrant {
		t.Fatal(m)
	}
	if m := members[inactive]; m.Capabilities.Read || m.CanGrant || !m.CanRevoke || m.GrantBlockedReason != "MEMBER_NOT_ACTIVE" {
		t.Fatal(m)
	}
	candidates, err := s.Candidates(ctx, p, org, project, MemberPageInput{Cut: first.Cut})
	if err != nil || len(candidates.Items) != 2 {
		t.Fatal(candidates, err)
	}
	for _, m := range candidates.Items {
		if m.UserID != viewer && m.UserID != orgViewer {
			t.Fatal("candidate scope leaked", m)
		}
		if !m.CanGrant || m.WriteCapReason == "" {
			t.Fatal("viewer cap not explained", m)
		}
	}
	viewerPrincipal := servicePrincipal(t, pool, viewer)
	_, err = s.Roster(ctx, viewerPrincipal, org, project, MemberPageInput{})
	accessFailure(t, err, 403, "FORBIDDEN")
	_, err = s.Candidates(ctx, p, otherOrg, project, MemberPageInput{})
	accessFailure(t, err, 404, "NOT_FOUND")
	accessExec(t, pool, "UPDATE organizations SET state='suspended' WHERE id=$1", org)
	readonly, err := s.Roster(ctx, p, org, project, MemberPageInput{})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range readonly.Items {
		if m.CanGrant || m.CanRevoke {
			t.Fatal("suspended org write", m)
		}
	}
}
