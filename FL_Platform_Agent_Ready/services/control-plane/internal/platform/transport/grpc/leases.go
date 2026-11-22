package transport

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	round "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/adapters"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func (s *Server) RenewLease(ctx context.Context, r *pb.RenewLeaseRequest) (*pb.RenewLeaseResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	var expires time.Time
	err = db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		l, err := scheduler.Lock(ctx, tx, id, r.TaskId, r.LeaseId, r.LeaseToken)
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "UPDATE tasks SET lease_until=clock_timestamp()+make_interval(secs=>$2) WHERE id=$1 RETURNING lease_until", r.TaskId, l.Config.LeaseSeconds).Scan(&expires)
	})
	return &pb.RenewLeaseResponse{LeaseExpiresAt: timestamppb.New(expires)}, rpcError(err)
}
func (s *Server) ReportFailure(ctx context.Context, r *pb.ReportFailureRequest) (*pb.ReportFailureResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	response := &pb.ReportFailureResponse{}
	hash := round.RequestHash(r)
	err = db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		found, err := round.Receipt(ctx, tx, id, "failure", r.IdempotencyKey, hash, response)
		if err != nil || found {
			return err
		}
		l, err := scheduler.Lock(ctx, tx, id, r.TaskId, r.LeaseId, r.LeaseToken)
		if err != nil {
			return err
		}
		if r.FailureCode != "TRAINING_FAILED" && r.FailureCode != "INCOMPATIBLE_MODEL" && r.FailureCode != "DATASET_MISMATCH" {
			return errors.New("INVALID_UPDATE")
		}
		if _, err = tx.Exec(ctx, "UPDATE tasks SET status='failed' WHERE id=$1", r.TaskId); err != nil {
			return err
		}
		if err = round.Event(ctx, tx, l.RunID, l.RoundID, id, r.FailureCode); err != nil {
			return err
		}
		response.Recorded = true
		return round.SaveReceipt(ctx, tx, id, "failure", r.IdempotencyKey, hash, response)
	})
	return response, rpcError(err)
}
