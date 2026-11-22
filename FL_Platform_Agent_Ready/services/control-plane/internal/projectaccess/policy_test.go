package projectaccess

import "testing"

func TestAccessPrecedenceAndCaps(t *testing.T) {
	base := Policy{Mode: "organization", GlobalRole: "researcher", OrganizationKind: "shared", OrganizationRole: "researcher", OrganizationState: "active", MembershipStatus: "active", Visibility: "private"}
	for _, test := range []struct {
		name                string
		change              func(*Policy)
		read, write, manage bool
		source              string
	}{
		{"private", func(*Policy) {}, false, false, false, "none"},
		{"creator", func(p *Policy) { p.Creator = true }, true, true, false, "creator"},
		{"visible only reads", func(p *Policy) { p.Visibility = "organization" }, true, false, false, "organization_read"},
		{"creator capped viewer", func(p *Policy) { p.Creator = true; p.GrantStatus = "active"; p.GrantRole = "viewer" }, true, false, false, "explicit_viewer"},
		{"denied creator visible", func(p *Policy) { p.Creator = true; p.Visibility = "organization"; p.GrantStatus = "revoked" }, false, false, false, "explicit_denied"},
		{"researcher grant", func(p *Policy) { p.GrantStatus = "active"; p.GrantRole = "researcher" }, true, true, false, "explicit_researcher"},
		{"global viewer cap", func(p *Policy) { p.GlobalRole = "viewer"; p.OrganizationRole = "owner" }, true, false, false, "organization_admin"},
		{"org viewer cap", func(p *Policy) { p.OrganizationRole = "viewer"; p.Creator = true }, true, false, false, "creator"},
		{"inherited admin", func(p *Policy) { p.OrganizationRole = "admin"; p.GrantStatus = "revoked" }, true, true, true, "organization_admin"},
		{"global admin no bypass", func(p *Policy) { p.GlobalRole = "admin"; p.MembershipStatus = "revoked" }, false, false, false, "none"},
		{"suspended organization", func(p *Policy) { p.OrganizationRole = "owner"; p.OrganizationState = "suspended" }, true, false, false, "organization_admin"},
		{"disabled identity", func(p *Policy) { p.Disabled = true; p.OrganizationRole = "owner" }, false, false, false, "none"},
	} {
		t.Run(test.name, func(t *testing.T) {
			in := base
			test.change(&in)
			got, source := Evaluate(in)
			if got.Read != test.read || got.CreateExperiment != test.write || got.ManageAccess != test.manage || got.Export != got.Read || got.StartRun || source != test.source {
				t.Fatalf("permissions %+v source %s", got, source)
			}
		})
	}
}
func TestLegacyPrivacyIgnoresOrganizationMetadata(t *testing.T) {
	in := Policy{Mode: "legacy_private", GlobalRole: "researcher", Owner: true, OrganizationRole: "viewer", OrganizationState: "suspended", MembershipStatus: "revoked", GrantStatus: "revoked"}
	got, source := Evaluate(in)
	if !got.Read || !got.StartRun || got.ManageAccess || source != "legacy_owner" {
		t.Fatal(got, source)
	}
	in.Owner = false
	in.GlobalRole = "admin"
	in.OrganizationRole = "owner"
	if got, _ = Evaluate(in); got.Read {
		t.Fatal("organization admin crossed legacy ownership")
	}
	in.Owner = true
	in.GlobalRole = "viewer"
	if got, _ = Evaluate(in); !got.Read || got.StartRun || got.CreateExperiment {
		t.Fatal("legacy viewer cap lost")
	}
}
func TestQueueGateAndValidation(t *testing.T) {
	err := allowed(Project{AccessMode: "organization", Capabilities: Capabilities{Read: true, CreateExperiment: true}}, QueueRun)
	if e, ok := err.(Failure); !ok || e.Status != 503 || e.Code != "ORG_AGENTS_NOT_READY" {
		t.Fatal(err)
	}
	for _, in := range []CreateInput{{"slug", ""}, {"UPPER", "Name"}, {"ok", "bad\x00name"}, {"ok", "\xff"}} {
		if _, err := validateCreate(in); err == nil {
			t.Fatal("invalid input accepted", in)
		}
	}
}
