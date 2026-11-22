package adapters

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"google.golang.org/protobuf/proto"
)

type Store struct{ Pool *pgxpool.Pool }

func RequestHash(request proto.Message) string {
	data, _ := proto.MarshalOptions{Deterministic: true}.Marshal(request)
	return artifact.Hash(data)
}
func Receipt(ctx context.Context, tx pgx.Tx, client, operation, key, hash string, response proto.Message) (bool, error) {
	if len(key) < 8 || len(key) > 128 {
		return false, errors.New("INVALID_IDEMPOTENCY_KEY")
	}
	// Serialize equal keys across runs before reading the immutable receipt.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", client+"/"+operation+"/"+key); err != nil {
		return false, err
	}
	var existing string
	var data []byte
	err := tx.QueryRow(ctx, "SELECT request_hash,response_bytes FROM rpc_receipts WHERE client_id=$1 AND operation=$2 AND idempotency_key=$3", client, operation, key).Scan(&existing, &data)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing != hash {
		return false, errors.New("IDEMPOTENCY_CONFLICT")
	}
	return true, proto.Unmarshal(data, response)
}
func SaveReceipt(ctx context.Context, tx pgx.Tx, client, operation, key, hash string, response proto.Message) error {
	data, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO rpc_receipts(client_id,operation,idempotency_key,request_hash,response_bytes) VALUES($1,$2,$3,$4,$5)", client, operation, key, hash, data)
	return err
}
func Event(ctx context.Context, tx pgx.Tx, run, round, actor, event string) error {
	_, err := tx.Exec(ctx, "INSERT INTO audit_events(run_id,round_id,actor_id,event_type) VALUES($1,$2,$3,$4)", run, round, actor, event)
	return err
}
func (s Store) Submit(ctx context.Context, client string, request *pb.SubmitUpdateRequest) (*pb.SubmitUpdateResponse, error) {
	response := &pb.SubmitUpdateResponse{}
	hash := RequestHash(request)
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		found, err := Receipt(ctx, tx, client, "submit", request.IdempotencyKey, hash, response)
		if err != nil || found {
			return err
		}
		l, err := scheduler.Lock(ctx, tx, client, request.TaskId, request.LeaseId, request.LeaseToken)
		if err != nil {
			return err
		}
		if l.Kind != "train" || request.RunId != l.RunID || int(request.RoundNo) != l.Round || int(request.BaseModelVersion) != l.Base {
			return errors.New("STALE_VERSION")
		}
		if request.ModelSchemaHash != l.Schema || request.UpdateArtifact == nil {
			return errors.New("INCOMPATIBLE_MODEL")
		}
		if request.SampleCount != 200 || len(request.Metrics) != 0 {
			return errors.New("INVALID_UPDATE")
		}
		meta, err := artifact.Read(ctx, tx, request.UpdateArtifact.ArtifactId)
		if err != nil {
			return err
		}
		if !proto.Equal(meta.Ref, request.UpdateArtifact) || meta.Ref.ModelSchemaHash != l.Schema {
			return errors.New("INCOMPATIBLE_MODEL")
		}
		var belongs bool
		err = tx.QueryRow(ctx, "SELECT uploaded_by_task=$2 FROM artifacts WHERE id=$1", meta.Ref.ArtifactId, request.TaskId).Scan(&belongs)
		if err != nil || !belongs {
			return errors.New("FORBIDDEN")
		}
		var duplicate bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM updates WHERE round_id=$1 AND client_id=$2 AND status='accepted')", l.RoundID, client).Scan(&duplicate)
		if err != nil {
			return err
		}
		if duplicate {
			return errors.New("DUPLICATE_UPDATE")
		}
		id := uuid.NewString()
		_, err = tx.Exec(ctx, `INSERT INTO updates(id,task_id,round_id,client_id,base_model_version,artifact_id,sample_count,idempotency_key,status,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'accepted',$9)`, id, request.TaskId, l.RoundID, client, l.Base, meta.Ref.ArtifactId, request.SampleCount, request.IdempotencyKey, hash)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE tasks SET status='submitted' WHERE id=$1", request.TaskId); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "UPDATE rounds SET accepted_count=accepted_count+1 WHERE id=$1", l.RoundID); err != nil {
			return err
		}
		if err = Event(ctx, tx, l.RunID, l.RoundID, client, "update.accepted"); err != nil {
			return err
		}
		response.Accepted = true
		response.UpdateId = id
		response.StatusCode = "ACCEPTED"
		return SaveReceipt(ctx, tx, client, "submit", request.IdempotencyKey, hash, response)
	})
	return response, err
}
