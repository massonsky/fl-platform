package adapters

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
)

type exportSnapshot struct {
	files     map[string][]byte
	key, hash string
	size      int64
	remaining int
}

func (s Store) AuthorizeExport(ctx context.Context, access Access, run string) error {
	return s.protected(ctx, access, func(ctx context.Context, tx pgx.Tx, user identity.User, acl bool) error {
		return checkProject(ctx, tx, access, user, acl, "run", run, projectaccess.Export)
	})
}

func (s Store) exportSnapshot(ctx context.Context, access Access, run string) (exportSnapshot, error) {
	snapshot := exportSnapshot{files: map[string][]byte{}, remaining: ExportMetadataLimit}
	err := s.protected(ctx, access, func(ctx context.Context, tx pgx.Tx, user identity.User, acl bool) error {
		if err := checkProject(ctx, tx, access, user, acl, "run", run, projectaccess.Export); err != nil {
			return err
		}
		var status string
		if err := tx.QueryRow(ctx, "SELECT status FROM runs WHERE id=$1", run).Scan(&status); err != nil {
			return err
		}
		if status != "completed" {
			return Failure{409, "EXPORT_NOT_READY"}
		}
		var artifactState string
		if err := tx.QueryRow(ctx, `SELECT a.storage_key,a.sha256_hex,a.size_bytes,a.state FROM model_versions m JOIN artifacts a ON a.id=m.artifact_id WHERE m.run_id=$1 ORDER BY m.version_no DESC LIMIT 1`, run).Scan(&snapshot.key, &snapshot.hash, &snapshot.size, &artifactState); err != nil {
			return err
		}
		if artifactState != "committed" {
			return Failure{503, "DEPENDENCY_UNAVAILABLE"}
		}
		if snapshot.size > ExportModelLimit {
			return Failure{409, "EXPORT_LIMIT_EXCEEDED"}
		}
		if snapshot.size <= 0 {
			return Failure{503, "DEPENDENCY_UNAVAILABLE"}
		}
		queries := []struct {
			name, query string
			array       bool
		}{
			{"config.json", `SELECT manifest_json AS data FROM runs WHERE id=$1`, false},
			{"run_manifest.json", `SELECT json_build_object('run_id',r.id,'config',r.manifest_json,'config_hash',r.manifest_hash,'dataset_manifest',p.dataset_manifest,'partition_manifest',p.partition_manifest,'runtime_manifest',p.runtime_manifest,'schema',p.schema_json) AS data FROM runs r JOIN model_presets p ON p.config_json->>'partition_manifest_hash'=r.manifest_json->>'partition_manifest_hash' WHERE r.id=$1 LIMIT 1`, false},
			{"metrics.json", `SELECT json_build_object('round',ro.round_no,'split',m.split,'name',m.metric_name,'value',m.metric_value) AS data FROM metric_points m JOIN rounds ro ON ro.id=m.round_id WHERE m.run_id=$1 ORDER BY ro.round_no,m.split,m.metric_name`, true},
			{"confusions.json", `SELECT json_build_object('round',ro.round_no,'validation',e.validation_confusion,'test',e.test_confusion) AS data FROM evaluations e JOIN rounds ro ON ro.id=e.round_id WHERE ro.run_id=$1 ORDER BY ro.round_no`, true},
			{"models.json", `SELECT json_build_object('version',m.version_no,'artifact_id',a.id,'sha256',a.sha256_hex,'size_bytes',a.size_bytes,'schema_hash',a.model_schema_hash,'update_ids',ro.frozen_update_ids) AS data FROM model_versions m JOIN artifacts a ON a.id=m.artifact_id LEFT JOIN rounds ro ON ro.id=m.produced_by_round_id WHERE m.run_id=$1 ORDER BY m.version_no`, true},
		}
		for _, item := range queries {
			data, err := collectExport(ctx, tx, item.query, run, &snapshot.remaining, item.array)
			if err != nil {
				return err
			}
			snapshot.files[item.name] = data
		}
		return nil
	})
	return snapshot, err
}

func collectExport(ctx context.Context, q db.Query, query, run string, remaining *int, array bool) ([]byte, error) {
	// Bound each returned row in PostgreSQL as well as the cumulative buffer. No
	// oversized JSON value is transferred just to discover its length in Go.
	rows, err := q.Query(ctx, `SELECT CASE WHEN octet_length(data::text)<=$2 THEN data ELSE NULL END FROM (`+query+`) export_rows`, run, *remaining)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buffer := exportBuffer{remaining: remaining}
	if array {
		if _, err = buffer.Write([]byte("[")); err != nil {
			return nil, err
		}
	}
	count := 0
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, Failure{409, "EXPORT_LIMIT_EXCEEDED"}
		}
		if count > 0 && array {
			if _, err = buffer.Write([]byte(",")); err != nil {
				return nil, err
			}
		}
		if _, err = buffer.Write(raw); err != nil {
			return nil, err
		}
		count++
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if !array && count != 1 {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	if array {
		if _, err = buffer.Write([]byte("]")); err != nil {
			return nil, err
		}
	}
	return buffer.Bytes(), nil
}
