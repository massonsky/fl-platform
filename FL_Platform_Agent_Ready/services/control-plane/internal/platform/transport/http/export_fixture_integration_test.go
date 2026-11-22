//go:build integration

package transport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

type exportBlobFunc func(context.Context, string, string, int64) ([]byte, error)

func (f exportBlobFunc) Get(ctx context.Context, key, hash string, size int64) ([]byte, error) {
	return f(ctx, key, hash, size)
}

type exportFixture struct {
	authorizationFixture
	access                      experiment.Access
	project, run, artifact, org string
	model                       []byte
}

func newExportFixture(t *testing.T, shared bool) exportFixture {
	t.Helper()
	f := exportFixture{authorizationFixture: projectFixture(t, shared), project: uuid.NewString(), artifact: uuid.NewString(), model: make([]byte, 3*exportChunkSize)}
	if _, err := rand.Read(f.model); err != nil {
		t.Fatal(err)
	}
	owner := f.users["owner"]
	f.access.Principal = owner.Principal()
	if shared {
		f.org = f.shared(t)
		f.access.OrganizationID = f.org
		store := projectaccess.Store{Pool: f.pool}
		if _, err := store.Create(context.Background(), owner.ID, f.org, f.project, projectaccess.CreateInput{Slug: "export-fixture", Name: "Export fixture"}); err != nil {
			t.Fatal(err)
		}
		authorizationExec(t, f.pool, "INSERT INTO organization_memberships(organization_id,user_id,role) VALUES($1,$2,'owner')", f.org, f.users["other"].ID)
		authorizationExec(t, f.pool, "UPDATE organization_memberships SET role='researcher' WHERE organization_id=$1 AND user_id=$2", f.org, owner.ID)
	} else {
		authorizationExec(t, f.pool, "INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,'export-fixture','Export fixture')", f.project, owner.ID)
	}
	_, f.run = projectResources(t, f.authorizationFixture, f.project, owner.ID)
	manifest := `{"rounds":1,"partition_manifest_hash":"fixture"}`
	authorizationExec(t, f.pool, "UPDATE runs SET status='completed',manifest_json=$2 WHERE id=$1", f.run, manifest)
	hash := sha256.Sum256(f.model)
	authorizationExec(t, f.pool, `INSERT INTO artifacts(id,storage_key,sha256_hex,size_bytes,codec,state,model_schema_hash) VALUES($1,'private/export-fixture.bin',$2,$3,'safetensors','committed',$4)`, f.artifact, hex.EncodeToString(hash[:]), len(f.model), strings.Repeat("b", 64))
	authorizationExec(t, f.pool, `INSERT INTO model_versions(run_id,version_no,artifact_id,model_schema_hash) VALUES($1,1,$2,$3)`, f.run, f.artifact, strings.Repeat("b", 64))
	authorizationExec(t, f.pool, `INSERT INTO model_presets(id,config_json,schema_json,dataset_manifest,partition_manifest,runtime_manifest,initial_artifact_id) VALUES('export-fixture',$1,'{}','{}','{}','{}',$2)`, manifest, f.artifact)
	round := uuid.NewString()
	authorizationExec(t, f.pool, "INSERT INTO rounds(id,run_id,round_no,base_model_version,status) VALUES($1,$2,1,0,'completed')", round, f.run)
	authorizationExec(t, f.pool, "INSERT INTO metric_points(run_id,round_id,split,metric_name,metric_value) VALUES($1,$2,'validation_global','accuracy',0.875)", f.run, round)
	f.s.Experiments.Blobs = exportBlobFunc(func(context.Context, string, string, int64) ([]byte, error) { return f.model, nil })
	return f
}

func (f exportFixture) request() *http.Request {
	r := httptest.NewRequest("GET", "/api/v1/runs/"+f.run+"/export", nil)
	r.Header.Set("Authorization", "Bearer "+f.users["owner"].ID)
	return r
}
