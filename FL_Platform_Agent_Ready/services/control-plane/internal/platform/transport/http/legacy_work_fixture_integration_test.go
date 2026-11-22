//go:build integration

package transport

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	config "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/domain"
)

// Upgrade only a named fixture; generic schema6/8 helpers remain unchanged.
func legacyWorkSchema(t *testing.T, f authorizationFixture, from int, through ...int) {
	t.Helper()
	limit := 9
	if len(through) > 0 {
		limit = through[0]
	}
	for i, file := range []string{"000007_organizations.up.sql", "000008_project_access.up.sql", "000009_legacy_lab_admission.up.sql"} {
		if i+7 <= from || i+7 > limit {
			continue
		}
		raw, err := os.ReadFile("/src/db/migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		authorizationExec(t, f.pool, string(raw))
	}
}

func legacyWorkFixture(t *testing.T) authorizationFixture {
	t.Helper()
	f := projectFixture(t, false)
	legacyWorkSchema(t, f, 6)
	configureCredentialLogin(t, f.s)
	return f
}

func legacyWorkRequest(f authorizationFixture, token, method, path, input, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(input))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	if method == "POST" {
		if key == "" {
			key = uuid.NewString()
		}
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	return w
}

func legacyWorkID(t *testing.T, w *httptest.ResponseRecorder, status int) string {
	t.Helper()
	legacyLabResponse(t, w, status, "")
	var result struct{ ID string }
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result.ID == "" {
		t.Fatal("missing work resource ID")
	}
	return result.ID
}

func legacyWorkGrant(t *testing.T, f authorizationFixture, user, status string, revision int) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"status": status, "revision": revision, "reason": "Isolated legacy work test"})
	w := legacyWorkRequest(f, f.users["admin"].ID, "PUT", "/admin/users/"+user+"/legacy-lab", string(body), "")
	legacyLabResponse(t, w, 200, "")
}

func legacyWorkCounts(t *testing.T, f authorizationFixture) string {
	t.Helper()
	return authorizationValue(t, f.pool, `SELECT (SELECT count(*) FROM projects)::text||'/'||
 (SELECT count(*) FROM experiments)::text||'/'||(SELECT count(*) FROM runs)::text||'/'||
 (SELECT count(*) FROM http_receipts)::text||'/'||(SELECT count(*) FROM audit_events)::text`)
}

func legacyWorkPreset(t *testing.T, f authorizationFixture) string {
	t.Helper()
	hash := strings.Repeat("a", 64)
	c := config.Config{SchemaVersion: 1, DatasetID: "digits", DatasetHash: hash, PartitionHash: hash,
		ModelName: "mlp64-32-10", ModelRevision: "nir-v1", SchemaHash: hash, InitialHash: hash,
		RuntimeHash: hash, Algorithm: "fedavg", Clients: 5, Rounds: 1, Seed: 11,
		Epochs: 1, Batch: 25, LR: 0.12, Optimizer: "sgd", Partition: "balanced", Selection: "all",
		Completion: config.Policy{Mode: "strict"}, RoundTimeout: 30, LeaseSeconds: 10}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(c)
	aid := uuid.NewString()
	authorizationExec(t, f.pool, `INSERT INTO artifacts(id,storage_key,sha256_hex,size_bytes,codec,state,model_schema_hash)
 VALUES($1,$2,$3::text,4,'safetensors','committed',$3::text)`, aid, "isolated/work/"+aid, hash)
	authorizationExec(t, f.pool, `INSERT INTO model_presets(id,config_json,schema_json,dataset_manifest,partition_manifest,runtime_manifest,initial_artifact_id)
 VALUES('legacy-work',$1,'{}','{}','{}','{}',$2)`, string(raw), aid)
	body, _ := json.Marshal(map[string]any{"name": "Isolated work", "config": json.RawMessage(raw)})
	return string(body)
}

func legacyWorkResource(t *testing.T, f authorizationFixture, owner string) (string, string, string) {
	t.Helper()
	p := uuid.NewString()
	authorizationExec(t, f.pool, "INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,$3,'Existing private work')", p, owner, "work-"+p)
	e, r := projectResources(t, f, p, owner)
	return p, e, r
}
