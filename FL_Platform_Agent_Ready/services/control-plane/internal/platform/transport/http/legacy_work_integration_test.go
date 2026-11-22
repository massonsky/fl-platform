//go:build integration

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func TestLegacyWorkExternalAccountsAndReplay(t *testing.T) {
	f := legacyWorkFixture(t)
	experimentBody := legacyWorkPreset(t, f)
	foreign, foreignExperiment, _ := legacyWorkResource(t, f, f.users["owner"].ID)
	for _, role := range []string{"researcher", "admin"} {
		t.Run(role, func(t *testing.T) {
			username := "external-" + role
			raw, _ := json.Marshal(map[string]any{"username": username, "name": "External account", "role": role, "password": "valid-new-password"})
			id := legacyWorkID(t, legacyWorkRequest(f, f.users["admin"].ID, "POST", "/admin/users", string(raw), ""), 201)
			token := legacyLabLogin(t, f, username, "valid-new-password")
			p, e, oldRun := legacyWorkResource(t, f, id)
			projectBody := `{"slug":"new-` + uuid.NewString() + `","name":"Admitted project"}`
			operations := []struct {
				path, body, key string
				status          int
			}{
				{"/projects", projectBody, uuid.NewString(), 201},
				{"/projects/" + p + "/experiments", experimentBody, uuid.NewString(), 201},
				{"/experiments/" + e + "/runs", "", uuid.NewString(), 202},
			}
			before := legacyWorkCounts(t, f)
			for _, op := range operations {
				legacyLabResponse(t, legacyWorkRequest(f, token, "POST", op.path, op.body, op.key), 403, "LEGACY_LAB_NOT_ADMITTED")
			}
			for _, path := range []string{"/projects/" + foreign + "/experiments", "/experiments/" + foreignExperiment + "/runs"} {
				body := ""
				if path == "/projects/"+foreign+"/experiments" {
					body = experimentBody
				}
				legacyLabResponse(t, legacyWorkRequest(f, token, "POST", path, body, ""), 404, "NOT_FOUND")
			}
			if legacyWorkCounts(t, f) != before {
				t.Fatal("denied work or receipt was committed")
			}
			legacyWorkGrant(t, f, id, "active", 0)
			for _, op := range operations {
				legacyWorkID(t, legacyWorkRequest(f, token, "POST", op.path, op.body, op.key), op.status)
			}
			accepted := legacyWorkCounts(t, f)
			manifest := authorizationValue(t, f.pool, "SELECT manifest_json::text||manifest_hash||status FROM runs WHERE id=$1", oldRun)
			legacyWorkGrant(t, f, id, "revoked", 1)
			for _, op := range operations {
				legacyLabResponse(t, legacyWorkRequest(f, token, "POST", op.path, op.body, op.key), 403, "LEGACY_LAB_NOT_ADMITTED")
				legacyLabResponse(t, legacyWorkRequest(f, token, "POST", op.path, op.body, ""), 403, "LEGACY_LAB_NOT_ADMITTED")
			}
			if legacyWorkCounts(t, f) != accepted || authorizationValue(t, f.pool, "SELECT manifest_json::text||manifest_hash||status FROM runs WHERE id=$1", oldRun) != manifest {
				t.Fatal("revocation/replay changed previously accepted work or scientific manifest")
			}
			legacyLabResponse(t, legacyWorkRequest(f, token, "GET", "/runs/"+oldRun, "", ""), 200, "")
			legacyLabResponse(t, legacyWorkRequest(f, token, "POST", "/runs/"+oldRun+"/cancel", "", ""), 200, "")
		})
	}
}

func TestLegacyWorkCompatibilityReadsExportAndCancel(t *testing.T) {
	for _, version := range []int{6, 8, 9} {
		f := newExportFixture(t, false)
		if version >= 8 {
			legacyWorkSchema(t, f.authorizationFixture, 6, version)
		}
		if version == 9 {
			legacyWorkGrant(t, f.authorizationFixture, f.users["owner"].ID, "revoked", 1)
		}
		if _, err := f.s.Experiments.Detail(context.Background(), f.access, "runs", f.run); err != nil {
			t.Fatal(err)
		}
		files, err := f.s.Experiments.Export(context.Background(), f.access, f.run)
		if err != nil || !bytes.Equal(files["final_model.bin"], f.model) {
			t.Fatal("legacy export requires admission", err)
		}
		e, queued := projectResources(t, f.authorizationFixture, f.project, f.users["owner"].ID)
		projectHTTP(t, f.authorizationFixture, "owner", "POST", "runs/"+queued+"/cancel", "", 200, "")
		before := legacyWorkCounts(t, f.authorizationFixture)
		status, code := 503, "DEPENDENCY_UNAVAILABLE"
		if version == 9 {
			status, code = 403, "LEGACY_LAB_NOT_ADMITTED"
		}
		projectHTTP(t, f.authorizationFixture, "owner", "POST", "projects", `{"slug":"closed-work","name":"Closed"}`, status, code)
		projectHTTP(t, f.authorizationFixture, "owner", "POST", "projects/"+f.project+"/experiments", `{}`, status, code)
		projectHTTP(t, f.authorizationFixture, "owner", "POST", "experiments/"+e+"/runs", "", status, code)
		if legacyWorkCounts(t, f.authorizationFixture) != before {
			t.Fatal("missing schema/admission created work")
		}
	}
}

func TestLegacyWorkPartialSchemaAndScopeOrder(t *testing.T) {
	f := legacyWorkFixture(t)
	p, e, _ := legacyWorkResource(t, f, f.users["owner"].ID)
	before := legacyWorkCounts(t, f)
	authorizationExec(t, f.pool, "ALTER TABLE legacy_lab_admission_events RENAME TO unavailable_admission_events")
	projectHTTP(t, f, "owner", "POST", "projects", `{"slug":"partial-work","name":"Partial"}`, 503, "DEPENDENCY_UNAVAILABLE")
	projectHTTP(t, f, "owner", "POST", "projects/"+p+"/experiments", `{}`, 503, "DEPENDENCY_UNAVAILABLE")
	projectHTTP(t, f, "owner", "POST", "experiments/"+e+"/runs", "", 503, "DEPENDENCY_UNAVAILABLE")
	projectHTTP(t, f, "other", "POST", "experiments/"+e+"/runs", "", 404, "NOT_FOUND")
	projectHTTP(t, f, "viewer", "POST", "projects", `{"slug":"viewer-work","name":"Viewer"}`, 403, "FORBIDDEN")
	if legacyWorkCounts(t, f) != before {
		t.Fatal("partial schema created work or receipt")
	}
}
