//go:build integration

package transport

import (
	"net/netip"
	"regexp"
	"testing"

	admin "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/administration/adapters"
)

// Explicit opt-in for credential-positive cases only. Never call this from the
// generic authorizationDB/project/export fixtures: schema6/8/9 negative cases
// and role-only writes must remain independent of credential schema/key/work.
func configureCredentialAdministration(t *testing.T, s *Server) admin.Service {
	t.Helper()
	if s == nil || s.Identity.Pool == nil {
		t.Fatal("requires disposable administration fixture")
	}
	name := s.Identity.Pool.Config().ConnConfig.Database
	if !regexp.MustCompile(`^ai_test_[0-9a-f]{32}$`).MatchString(name) ||
		authorizationValue(t, s.Identity.Pool, "SELECT current_database()") != name {
		t.Fatal("credential fixture database is not the explicit created test database")
	}
	if s.Identity.Limits != nil || s.Identity.Work != nil {
		t.Fatal("configure credential fixture exactly once")
	}
	// A2 helper requires marker/configured release_check/host/secrets and loads
	// only the exact000010 SQL file, with a fresh random key and a real WorkPool.
	configureCredentialLogin(t, s)
	return admin.Service{Pool: s.Identity.Pool, Limits: s.Identity.Limits, Work: s.Identity.Work}
}

// Direct application tests supply a server-resolved fixture address. It is not
// serialized to JSON or accepted from the public mutation body.
func administrationCredentialCommand(op, target, key, fingerprint string, in admin.Input) admin.CredentialMutation {
	return admin.CredentialMutation{Client: netip.MustParseAddr("192.0.2.2"), Operation: op, Target: target,
		Key: key, Fingerprint: fingerprint, Input: in}
}
