package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	config "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/domain"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func (s Store) Active(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, "SELECT id::text FROM runs WHERE status IN ('queued','running') ORDER BY requested_at LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func failRun(ctx context.Context, tx pgx.Tx, run, round, reason string) error {
	if _, err := tx.Exec(ctx, "UPDATE runs SET status='failed',failure_code=$2,finished_at=now() WHERE id=$1", run, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE rounds SET status='failed',failure_code=$2,aggregation_epoch=aggregation_epoch+1 WHERE id=$1", round, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE tasks SET status='cancelled' WHERE round_id=$1 AND status IN ('available','leased')", round); err != nil {
		return err
	}
	return Event(ctx, tx, run, round, "orchestrator", reason)
}
func (s Store) Advance(ctx context.Context, run string) (*Work, error) {
	var work *Work
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT status,manifest_json FROM runs WHERE id=$1 FOR UPDATE", run).Scan(&status, &raw); err != nil {
			return err
		}
		if status != "queued" && status != "running" {
			return nil
		}
		var c config.Config
		if err := json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if status == "queued" {
			ready, err := startRun(ctx, tx, run, c)
			if err != nil || !ready {
				return err
			}
		}
		var round, phase string
		var number, accepted int
		var expired bool
		err := tx.QueryRow(ctx, "SELECT id::text,round_no,status,accepted_count,deadline_at<=clock_timestamp() FROM rounds WHERE run_id=$1 ORDER BY round_no DESC LIMIT 1 FOR UPDATE", run).Scan(&round, &number, &phase, &accepted, &expired)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && phase == "completed" {
			return createRound(ctx, tx, run, number+1, c)
		}
		if err != nil {
			return err
		}
		if phase == "collecting" {
			if accepted < 5 && !expired {
				return retryTasks(ctx, tx, round)
			}
			minimum := 5
			if c.Completion.Mode == "quorum" {
				minimum = c.Completion.MinClients
			}
			if accepted < minimum {
				return failRun(ctx, tx, run, round, "ROUND_TIMEOUT")
			}
			if _, err = tx.Exec(ctx, `UPDATE rounds SET status='aggregating',frozen_update_ids=ARRAY(SELECT id FROM updates WHERE round_id=$1 AND status='accepted' ORDER BY client_id) WHERE id=$1`, round); err != nil {
				return err
			}
			if _, err = tx.Exec(ctx, "UPDATE tasks SET status='cancelled' WHERE round_id=$1 AND status IN ('available','leased')", round); err != nil {
				return err
			}
			phase = "aggregating"
		}
		if phase == "evaluating" {
			if expired {
				return failRun(ctx, tx, run, round, "EVALUATION_TIMEOUT")
			}
			return retryTasks(ctx, tx, round)
		}
		if phase != "aggregating" {
			return nil
		}
		w := &Work{RunID: run, RoundID: round, Round: number, Owner: uuid.NewString(), Schema: c.SchemaHash}
		err = tx.QueryRow(ctx, `UPDATE rounds SET aggregation_owner=$2,aggregation_epoch=aggregation_epoch+1,aggregation_until=clock_timestamp()+interval '60 seconds' WHERE id=$1 AND (aggregation_until IS NULL OR aggregation_until<=clock_timestamp()) RETURNING aggregation_epoch,frozen_update_ids::text[]`, round, w.Owner).Scan(&w.Epoch, &w.Updates)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		work = w
		return nil
	})
	return work, err
}
func startRun(ctx context.Context, tx pgx.Tx, run string, c config.Config) (bool, error) {
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM clients WHERE shard_index IS NOT NULL AND status='online' AND last_seen_at>now()-interval '30 seconds'").Scan(&count); err != nil {
		return false, err
	}
	if count != 5 {
		return false, nil
	}
	var artifactID string
	err := tx.QueryRow(ctx, "SELECT initial_artifact_id::text FROM model_presets WHERE config_json->>'partition_manifest_hash'=$1 AND config_json->>'initial_weights_hash'=$2 LIMIT 1", c.PartitionHash, c.InitialHash).Scan(&artifactID)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO model_versions(run_id,version_no,artifact_id,model_schema_hash) VALUES($1,0,$2,$3)", run, artifactID, c.SchemaHash); err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, "UPDATE runs SET status='running',started_at=now() WHERE id=$1", run)
	return err == nil, err
}
func createRound(ctx context.Context, tx pgx.Tx, run string, number int, c config.Config) error {
	if number > c.Rounds {
		return errors.New("invalid next round")
	}
	var round string
	err := tx.QueryRow(ctx, `INSERT INTO rounds(run_id,round_no,base_model_version,status,selected_count,deadline_at,started_at) VALUES($1,$2,$2-1,'collecting',5,clock_timestamp()+make_interval(secs=>$3),now()) RETURNING id::text`, run, number, c.RoundTimeout).Scan(&round)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO tasks(round_id,client_id,status) SELECT $1,id,'available' FROM clients WHERE shard_index IS NOT NULL AND status<>'disabled'", round); err != nil {
		return err
	}
	return Event(ctx, tx, run, round, "orchestrator", "round.started")
}
func retryTasks(ctx context.Context, tx pgx.Tx, round string) error {
	if _, err := tx.Exec(ctx, "UPDATE tasks SET status='expired' WHERE round_id=$1 AND status='leased' AND lease_until<=clock_timestamp()", round); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO tasks(round_id,client_id,kind,attempt,status) SELECT t.round_id,t.client_id,t.kind,t.attempt+1,'available' FROM tasks t WHERE t.round_id=$1 AND t.status IN ('expired','failed') AND t.attempt<3 AND NOT EXISTS(SELECT 1 FROM tasks newer WHERE newer.round_id=t.round_id AND newer.client_id=t.client_id AND newer.kind=t.kind AND newer.attempt>t.attempt)`, round)
	return err
}
