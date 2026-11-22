package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	cfg "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/domain"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

type Store struct{ Pool *pgxpool.Pool }
type Lease struct {
	RunID, RoundID, Kind, Schema, ConfigHash string
	Round, Base, Attempt, Shard              int
	Config                                   cfg.Config
}

func Lock(ctx context.Context, tx pgx.Tx, client, task, lease, token string) (Lease, error) {
	var l Lease
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT r.id::text FROM runs r JOIN rounds ro ON ro.run_id=r.id JOIN tasks t ON t.round_id=ro.id WHERE t.id=$1 FOR UPDATE OF r`, task).Scan(&l.RunID)
	if err != nil {
		return l, err
	}
	err = tx.QueryRow(ctx, `SELECT ro.id::text,ro.round_no,ro.base_model_version,t.kind,t.attempt,c.shard_index,r.manifest_json,r.manifest_hash FROM tasks t JOIN rounds ro ON ro.id=t.round_id JOIN runs r ON r.id=ro.run_id JOIN clients c ON c.id=t.client_id WHERE t.id=$1 AND t.client_id=$2 AND t.lease_id=$3 AND t.lease_token_hash=$4 AND t.status='leased' AND t.lease_until>clock_timestamp() AND r.status='running' AND ((t.kind='train' AND ro.status='collecting' AND ro.deadline_at>clock_timestamp()) OR (t.kind='evaluate' AND ro.status='evaluating')) FOR UPDATE OF ro,t`, task, client, lease, identity.Hash(token)).Scan(&l.RoundID, &l.Round, &l.Base, &l.Kind, &l.Attempt, &l.Shard, &raw, &l.ConfigHash)
	if err != nil {
		return l, errors.New("LEASE_EXPIRED")
	}
	err = json.Unmarshal(raw, &l.Config)
	l.Schema = l.Config.SchemaHash
	return l, err
}
func (s Store) Acquire(ctx context.Context, client string) (*pb.TaskAssignment, error) {
	var result *pb.TaskAssignment
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		var task, run string
		err := tx.QueryRow(ctx, `SELECT t.id::text,r.id::text FROM tasks t JOIN rounds ro ON ro.id=t.round_id JOIN runs r ON r.id=ro.run_id WHERE t.client_id=$1 AND t.status='available' AND r.status='running' ORDER BY t.created_at,t.id LIMIT 1`, client).Scan(&task, &run)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var status string
		if err = tx.QueryRow(ctx, "SELECT status FROM runs WHERE id=$1 FOR UPDATE", run).Scan(&status); err != nil {
			return err
		}
		if status != "running" {
			return nil
		}
		var raw, schema []byte
		var round, base, attempt, shard int
		var kind, hash, artifactID string
		var expires time.Time
		err = tx.QueryRow(ctx, `SELECT r.manifest_json,r.manifest_hash,ro.round_no,ro.base_model_version,t.attempt,t.kind,c.shard_index FROM tasks t JOIN rounds ro ON ro.id=t.round_id JOIN runs r ON r.id=ro.run_id JOIN clients c ON c.id=t.client_id WHERE t.id=$1 AND t.status='available' AND ((t.kind='train' AND ro.status='collecting' AND ro.deadline_at>clock_timestamp()) OR (t.kind='evaluate' AND ro.status='evaluating')) FOR UPDATE OF ro,t`, task).Scan(&raw, &hash, &round, &base, &attempt, &kind, &shard)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		var config cfg.Config
		if err = json.Unmarshal(raw, &config); err != nil {
			return err
		}
		version := base
		if kind == "evaluate" {
			version = round
		}
		err = tx.QueryRow(ctx, "SELECT artifact_id::text FROM model_versions WHERE run_id=$1 AND version_no=$2", run, version).Scan(&artifactID)
		if err != nil {
			return err
		}
		meta, err := artifact.Read(ctx, tx, artifactID)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, "SELECT schema_json FROM model_presets WHERE config_json->>'partition_manifest_hash'=$1 LIMIT 1", config.PartitionHash).Scan(&schema)
		if err != nil {
			return err
		}
		modelSchema := &pb.ModelSchema{}
		if err = protojson.Unmarshal(schema, modelSchema); err != nil {
			return err
		}
		token, lease := identity.Token(), uuid.NewString()
		err = tx.QueryRow(ctx, `UPDATE tasks SET status='leased',lease_id=$2,lease_token_hash=$3,lease_until=clock_timestamp()+make_interval(secs=>$4) WHERE id=$1 RETURNING lease_until`, task, lease, identity.Hash(token), config.LeaseSeconds).Scan(&expires)
		if err != nil {
			return err
		}
		result = &pb.TaskAssignment{TaskId: task, LeaseId: lease, LeaseToken: token, LeaseExpiresAt: timestamppb.New(expires), RunId: run, RoundNo: uint32(round), BaseModelVersion: uint64(version), GlobalModel: meta.Ref, ConfigHash: hash, ModelSchema: modelSchema, Attempt: uint32(attempt), TaskKind: kind, ClientIndex: uint32(shard), EvaluateTest: round == config.Rounds, Training: &pb.TrainingConfig{Algorithm: config.Algorithm, Mu: config.Mu, LocalEpochs: uint32(config.Epochs), BatchSize: uint32(config.Batch), LearningRate: config.LR, Seed: uint64(config.Seed), DatasetId: config.DatasetID, PartitionManifestHash: config.PartitionHash, ModelName: config.ModelName, ModelRevision: config.ModelRevision, Partition: config.Partition}}
		return nil
	})
	return result, err
}
