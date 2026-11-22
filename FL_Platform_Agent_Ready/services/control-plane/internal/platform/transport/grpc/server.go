package transport

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	participant "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/participant/adapters"
	round "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/adapters"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
)

type Server struct {
	pb.UnimplementedParticipantServiceServer
	pb.UnimplementedArtifactServiceServer
	Pool      *pgxpool.Pool
	Registry  *participant.Registry
	Blobs     *artifact.Store
	Scheduler scheduler.Store
	Rounds    round.Store
}

func rpcError(err error) error {
	if err == nil {
		return nil
	}
	name := err.Error()
	code := codes.FailedPrecondition
	switch name {
	case "UNAUTHORIZED":
		code = codes.Unauthenticated
	case "FORBIDDEN":
		code = codes.PermissionDenied
	case "IDEMPOTENCY_CONFLICT", "DUPLICATE_UPDATE":
		code = codes.AlreadyExists
	case "INVALID_CONFIG", "INVALID_UPDATE", "INVALID_EVALUATION", "INVALID_IDEMPOTENCY_KEY", "MODEL_SIZE_MISMATCH", "NON_FINITE_MODEL", "INVALID_ARTIFACT":
		code = codes.InvalidArgument
	case "LEASE_EXPIRED", "STALE_VERSION", "INCOMPATIBLE_MODEL", "EARLY_TEST_EVALUATION":
	default:
		name = "INTERNAL"
		code = codes.Internal
	}
	result := status.New(code, name)
	detail := &pb.ErrorDetail{ErrorCode: name, RequestId: uuid.NewString(), Retryable: code == codes.Internal}
	with, detailErr := result.WithDetails(detail)
	if detailErr != nil {
		return result.Err()
	}
	return with.Err()
}
func (s *Server) shard(ctx context.Context) (int, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	headers := md.Get("authorization")
	if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
		return 0, errors.New("UNAUTHORIZED")
	}
	return s.Registry.Shard(strings.TrimPrefix(headers[0], "Bearer "))
}
func (s *Server) client(ctx context.Context, claimed string) (string, error) {
	shard, err := s.shard(ctx)
	if err != nil {
		return "", err
	}
	id, err := s.Registry.Client(ctx, shard)
	if err != nil {
		return "", errors.New("UNAUTHORIZED")
	}
	if claimed != "" && claimed != id {
		return "", errors.New("FORBIDDEN")
	}
	return id, nil
}
func (s *Server) Register(ctx context.Context, r *pb.RegisterRequest) (*pb.RegisterResponse, error) {
	shard, err := s.shard(ctx)
	if err != nil {
		return nil, rpcError(err)
	}
	id, err := s.Registry.Register(ctx, shard, r)
	return &pb.RegisterResponse{ClientId: id, ProtocolVersion: 1, HeartbeatIntervalSeconds: 10}, rpcError(err)
}
func (s *Server) Heartbeat(ctx context.Context, r *pb.HeartbeatRequest) (*pb.HeartbeatResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	_, err = s.Pool.Exec(ctx, "UPDATE clients SET last_seen_at=now(),status='online' WHERE id=$1 AND status<>'disabled'", id)
	if err != nil {
		return nil, rpcError(err)
	}
	stop := false
	if r.CurrentTaskId != "" {
		err = s.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM tasks t JOIN rounds ro ON ro.id=t.round_id JOIN runs r ON r.id=ro.run_id WHERE t.id=$1 AND t.client_id=$2 AND t.status='leased' AND t.lease_until>now() AND r.status='running')`, r.CurrentTaskId, id).Scan(&stop)
	}
	return &pb.HeartbeatResponse{ShouldStop: stop, ServerTime: timestamppb.Now()}, rpcError(err)
}
func (s *Server) AcquireTask(ctx context.Context, r *pb.AcquireTaskRequest) (*pb.AcquireTaskResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	task, err := s.Scheduler.Acquire(ctx, id)
	return &pb.AcquireTaskResponse{HasTask: task != nil, Task: task, RetryAfterSeconds: 1}, rpcError(err)
}
func (s *Server) SubmitUpdate(ctx context.Context, r *pb.SubmitUpdateRequest) (*pb.SubmitUpdateResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	response, err := s.Rounds.Submit(ctx, id, r)
	return response, rpcError(err)
}
func (s *Server) SubmitEvaluation(ctx context.Context, r *pb.SubmitEvaluationRequest) (*pb.SubmitEvaluationResponse, error) {
	id, err := s.client(ctx, r.ClientId)
	if err != nil {
		return nil, rpcError(err)
	}
	response, err := s.Rounds.Evaluate(ctx, id, r)
	return response, rpcError(err)
}
