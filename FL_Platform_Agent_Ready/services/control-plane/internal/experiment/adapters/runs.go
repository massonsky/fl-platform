package adapters

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

const runJSON = `json_build_object('id',r.id,'project_id',e.project_id,'experiment_id',r.experiment_id,'status',r.status,'config_hash',r.manifest_hash,'total_rounds',(r.manifest_json->>'rounds')::int,'completed_rounds',(SELECT count(*) FROM rounds ro WHERE ro.run_id=r.id AND ro.status='completed'),'latest_model_version',(SELECT max(version_no) FROM model_versions WHERE run_id=r.id),'requested_at',r.requested_at,'failure_code',r.failure_code)`

func Run(ctx context.Context, q db.Query, id string) (json.RawMessage, error) {
	var raw json.RawMessage
	err := q.QueryRow(ctx, "SELECT "+runJSON+" FROM runs r JOIN experiments e ON e.id=r.experiment_id WHERE r.id=$1", id).Scan(&raw)
	return raw, err
}
func queueRun(ctx context.Context, tx pgx.Tx, experiment, user string) (Result, error) {
	var id string
	result := Result{Status: 202}
	err := tx.QueryRow(ctx, `INSERT INTO runs(experiment_id,created_by,status,manifest_json,manifest_hash) SELECT id,$2,'queued',config_json,config_hash FROM experiments WHERE id=$1 RETURNING id::text`, experiment, user).Scan(&id)
	if err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO audit_events(run_id,actor_id,event_type) VALUES($1,$2,'run.queued')", id, user); err != nil {
		return result, err
	}
	result.Body, err = Run(ctx, tx, id)
	return result, err
}
func cancelRun(ctx context.Context, tx pgx.Tx, id string) (Result, error) {
	result := Result{Status: 200}
	var status string
	err := tx.QueryRow(ctx, "SELECT status FROM runs WHERE id=$1 FOR UPDATE", id).Scan(&status)
	if err != nil {
		return result, err
	}
	if status != "queued" && status != "running" && status != "cancelled" {
		return result, Failure{409, "INVALID_STATE"}
	}
	if status != "cancelled" {
		if _, err = tx.Exec(ctx, "UPDATE runs SET status='cancelled',finished_at=now() WHERE id=$1", id); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, "UPDATE rounds SET status='cancelled',aggregation_epoch=aggregation_epoch+1 WHERE run_id=$1 AND status NOT IN ('completed','failed','cancelled')", id); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, "UPDATE tasks SET status='cancelled' WHERE round_id IN (SELECT id FROM rounds WHERE run_id=$1) AND status IN ('available','leased')", id); err != nil {
			return result, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO audit_events(run_id,actor_id,event_type) VALUES($1,'researcher','run.cancelled')", id); err != nil {
			return result, err
		}
	}
	result.Body, err = Run(ctx, tx, id)
	return result, err
}
