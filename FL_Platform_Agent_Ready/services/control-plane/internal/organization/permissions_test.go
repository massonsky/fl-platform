package organization

import "testing"

func TestPermissionsIntersectLegacyAndMembership(t *testing.T) {
	for _, test := range []struct {
		name, globalRole, role, status, state string
		disabled                              bool
		want                                  Permissions
	}{
		{"owner", "researcher", "owner", "active", "active", false, Permissions{true, true, true}},
		{"org admin", "researcher", "admin", "active", "active", false, Permissions{true, true, true}},
		{"researcher", "researcher", "researcher", "active", "active", false, Permissions{true, true, false}},
		{"org viewer", "admin", "viewer", "active", "active", false, Permissions{true, false, false}},
		{"legacy viewer owner", "viewer", "owner", "active", "active", false, Permissions{true, false, false}},
		{"disabled owner", "admin", "owner", "active", "active", true, Permissions{}},
		{"revoked owner", "admin", "owner", "revoked", "active", false, Permissions{}},
		{"suspended member", "researcher", "owner", "suspended", "active", false, Permissions{}},
		{"suspended organization", "admin", "owner", "active", "suspended", false, Permissions{true, false, false}},
		{"platform admin without membership", "admin", "", "", "active", false, Permissions{}},
		{"unknown global role", "root", "owner", "active", "active", false, Permissions{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := PermissionsFor(test.globalRole, test.disabled, test.role, test.status, test.state)
			if got != test.want {
				t.Fatalf("permissions %+v, expected %+v", got, test.want)
			}
		})
	}
}

func TestAdministratorCannotChangeOrGrantOwner(t *testing.T) {
	for _, pair := range [][2]string{{"owner", "viewer"}, {"viewer", "owner"}, {"owner", "owner"}} {
		if canChangeMember("admin", pair[0], pair[1]) {
			t.Fatalf("admin allowed owner change %v", pair)
		}
	}
	if !canChangeMember("admin", "viewer", "researcher") || !canChangeMember("owner", "admin", "owner") {
		t.Fatal("legitimate membership change denied")
	}
}
