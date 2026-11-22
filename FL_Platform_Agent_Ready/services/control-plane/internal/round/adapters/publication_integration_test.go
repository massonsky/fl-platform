//go:build integration

package adapters

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// The test creates and drops its own database; never deletes application rows.
func disposable(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "flrp_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	for _, file := range []string{"000001_init.up.sql", "000002_runtime.up.sql"} {
		data, err := os.ReadFile(filepath.Join("/src/db/migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}
func claim(t *testing.T, pool *pgxpool.Pool) Work {
	t.Helper()
	ctx := context.Background()
	run, round, owner := uuid.NewString(), uuid.NewString(), uuid.NewString()
	user, project, experiment, artifactID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	statements := []struct {
		sql  string
		args []any
	}{
		{"INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,'test','researcher')", []any{user, user}},
		{"INSERT INTO projects(id,owner_id,slug,name) VALUES($1,$2,$3,'test')", []any{project, user, project}},
		{"INSERT INTO experiments(id,project_id,name,config_json,config_hash) VALUES($1,$2,'test','{}','test')", []any{experiment, project}},
		{"INSERT INTO runs(id,experiment_id,created_by,status,manifest_json,manifest_hash) VALUES($1,$2,$3,'running','{\"round_timeout_seconds\":180}','test')", []any{run, experiment, user}},
		{"INSERT INTO rounds(id,run_id,round_no,base_model_version,status,aggregation_owner,aggregation_epoch,aggregation_until,frozen_update_ids) VALUES($1,$2,1,0,'aggregating',$3,2,now()+interval '60 seconds','{}')", []any{round, run, owner}},
		{"INSERT INTO artifacts(id,storage_key,sha256_hex,size_bytes,codec,model_schema_hash,state) VALUES($1,$3,$2,8,'FLOAT32_LE',$4,'committed')", []any{artifactID, strings.Repeat("a", 64), artifactID, strings.Repeat("a", 64)}},
		{"INSERT INTO model_versions(run_id,version_no,artifact_id,model_schema_hash) VALUES($1,0,$2,$3)", []any{run, artifactID, strings.Repeat("a", 64)}},
	}
	for _, s := range statements {
		if _, err := pool.Exec(ctx, s.sql, s.args...); err != nil {
			t.Fatal(err)
		}
	}
	return Work{RunID: run, RoundID: round, Owner: owner, Round: 1, Epoch: 2, Schema: strings.Repeat("a", 64), Updates: []string{}}
}
func TestPostgresFencedPublication(t *testing.T) {
	pool := disposable(t)
	ctx := context.Background()
	store := Store{Pool: pool}
	_, err := pool.Exec(ctx, "INSERT INTO clients(display_name,identity_fingerprint,software_version,shard_index) VALUES('eval','test','test',0)")
	if err != nil {
		t.Fatal(err)
	}
	count := func(w Work) int {
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM model_versions WHERE run_id=$1 AND version_no=1", w.RunID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, condition := range []string{"wrong_owner", "wrong_epoch", "expired", "cancelled"} {
		t.Run(condition, func(t *testing.T) {
			w := claim(t, pool)
			switch condition {
			case "wrong_owner":
				w.Owner = uuid.NewString()
			case "wrong_epoch":
				w.Epoch = 1
			case "expired":
				_, err = pool.Exec(ctx, "UPDATE rounds SET aggregation_until=now()-interval '1 second' WHERE id=$1", w.RoundID)
			case "cancelled":
				_, err = pool.Exec(ctx, "UPDATE runs SET status='cancelled' WHERE id=$1", w.RunID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = store.Publish(ctx, w, "test/"+uuid.NewString(), strings.Repeat("a", 64), 8); err != nil {
				t.Fatal(err)
			}
			if count(w) != 0 {
				t.Fatal("stale publication committed")
			}
		})
	}
	w := claim(t, pool)
	var group sync.WaitGroup
	failures := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			failures <- store.Publish(ctx, w, "test/"+uuid.NewString(), strings.Repeat("a", 64), 8)
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count(w) != 1 {
		t.Fatal("double publication")
	}
	var phase string
	var tasks int
	if err = pool.QueryRow(ctx, "SELECT status FROM rounds WHERE id=$1", w.RoundID).Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM tasks WHERE round_id=$1 AND kind='evaluate'", w.RoundID).Scan(&tasks); err != nil {
		t.Fatal(err)
	}
	if phase != "evaluating" || tasks != 1 {
		t.Fatal("publication and evaluation task not atomic")
	}
	if _, err = pool.Exec(ctx, "UPDATE runs SET status='cancelled' WHERE id=$1", w.RunID); err != nil {
		t.Fatal(err)
	}
	if count(w) != 1 {
		t.Fatal("cancel removed committed model")
	}
}
