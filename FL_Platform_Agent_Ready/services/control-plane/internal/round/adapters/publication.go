package adapters

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func (s Store) Inputs(ctx context.Context, w Work) ([]Input, error) {
	rows, err := s.Pool.Query(ctx, `SELECT a.storage_key,a.sha256_hex,a.model_schema_hash,a.size_bytes,u.sample_count FROM updates u JOIN artifacts a ON a.id=u.artifact_id WHERE u.id=ANY($1::uuid[]) AND u.round_id=$2 AND u.base_model_version=$3 AND u.status='accepted' ORDER BY u.client_id`, w.Updates, w.RoundID, w.Round-1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var inputs []Input
	for rows.Next() {
		var input Input
		if err = rows.Scan(&input.Key, &input.Hash, &input.Schema, &input.Size, &input.Count); err != nil {
			return nil, err
		}
		inputs = append(inputs, input)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) != len(w.Updates) {
		return nil, errors.New("frozen membership mismatch")
	}
	return inputs, nil
}
func (s Store) Publish(ctx context.Context, w Work, key, hash string, size int) error {
	return db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, "SELECT status FROM runs WHERE id=$1 FOR UPDATE", w.RunID).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return nil
		}
		var valid bool
		err := tx.QueryRow(ctx, `SELECT status='aggregating' AND aggregation_owner=$2 AND aggregation_epoch=$3 AND aggregation_until>clock_timestamp() FROM rounds WHERE id=$1 FOR UPDATE`, w.RoundID, w.Owner, w.Epoch).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return nil
		}
		id, err := artifact.Record(ctx, tx, key, hash, w.Schema, size, nil, nil)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO model_versions(run_id,version_no,produced_by_round_id,artifact_id,model_schema_hash) VALUES($1,$2,$3,$4,$5)", w.RunID, w.Round, w.RoundID, id, w.Schema); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE rounds SET status='evaluating',committed_at=now(),deadline_at=clock_timestamp()+make_interval(secs=>(SELECT (manifest_json->>'round_timeout_seconds')::int FROM runs WHERE id=$2)) WHERE id=$1`, w.RoundID, w.RunID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO tasks(round_id,client_id,kind,status) SELECT $1,id,'evaluate','available' FROM clients WHERE shard_index=0", w.RoundID); err != nil {
			return err
		}
		return Event(ctx, tx, w.RunID, w.RoundID, "orchestrator", "model.published")
	})
}
