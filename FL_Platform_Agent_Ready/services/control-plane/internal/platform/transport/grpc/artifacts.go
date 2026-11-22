package transport

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	aggregate "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/aggregation/domain"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"io"
)

func (s *Server) Upload(stream grpc.ClientStreamingServer[pb.ArtifactChunk, pb.UploadReceipt]) error {
	ctx := stream.Context()
	client, err := s.client(ctx, "")
	if err != nil {
		return rpcError(err)
	}
	first, err := stream.Recv()
	if err != nil {
		return rpcError(errors.New("INVALID_ARTIFACT"))
	}
	if first.TotalSizeBytes != 9640 || first.Codec != "FLOAT32_LE" || len(first.UploadId) < 8 || len(first.UploadId) > 128 {
		return rpcError(errors.New("INVALID_ARTIFACT"))
	}
	var lease scheduler.Lease
	replay, err := s.uploadReceipt(ctx, client, first)
	if err != nil {
		return rpcError(err)
	}
	if replay == nil {
		err = db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
			var e error
			lease, e = scheduler.Lock(ctx, tx, client, first.TaskId, first.LeaseId, first.LeaseToken)
			return e
		})
		if err != nil {
			return rpcError(err)
		}
		if lease.Kind != "train" || lease.Schema != first.ModelSchemaHash {
			return rpcError(errors.New("INCOMPATIBLE_MODEL"))
		}
	}
	data := make([]byte, 0, int(first.TotalSizeBytes))
	header := proto.Clone(first).(*pb.ArtifactChunk)
	header.Data = nil
	header.Offset = 0
	chunk := first
	for {
		if len(chunk.Data) == 0 || len(chunk.Data) > 1<<20 || len(chunk.Data)%4 != 0 || chunk.Offset != uint64(len(data)) || len(data)+len(chunk.Data) > int(first.TotalSizeBytes) {
			return rpcError(errors.New("INVALID_ARTIFACT"))
		}
		metadata := proto.Clone(chunk).(*pb.ArtifactChunk)
		metadata.Data = nil
		metadata.Offset = 0
		if !proto.Equal(header, metadata) {
			return rpcError(errors.New("INVALID_ARTIFACT"))
		}
		data = append(data, chunk.Data...)
		chunk, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err = aggregate.Validate(data, int(first.TotalSizeBytes)); err != nil {
		return rpcError(err)
	}
	if artifact.Hash(data) != first.ExpectedSha256Hex {
		return rpcError(errors.New("INVALID_ARTIFACT"))
	}
	if replay != nil {
		return stream.SendAndClose(&pb.UploadReceipt{Artifact: replay})
	}
	key, err := s.Blobs.Put(ctx, data)
	if err != nil {
		return rpcError(err)
	}
	var meta artifact.Metadata
	err = db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := scheduler.Lock(ctx, tx, client, first.TaskId, first.LeaseId, first.LeaseToken); err != nil {
			return err
		}
		var id string
		err := tx.QueryRow(ctx, "SELECT id::text FROM artifacts WHERE uploaded_by_task=$1 AND upload_id=$2", first.TaskId, first.UploadId).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			id, err = artifact.Record(ctx, tx, key, artifact.Hash(data), first.ModelSchemaHash, len(data), &first.TaskId, &first.UploadId)
		}
		if err != nil {
			return err
		}
		meta, err = artifact.Read(ctx, tx, id)
		if err != nil {
			return err
		}
		if meta.Ref.Sha256Hex != first.ExpectedSha256Hex {
			return errors.New("IDEMPOTENCY_CONFLICT")
		}
		return nil
	})
	if err != nil {
		return rpcError(err)
	}
	return stream.SendAndClose(&pb.UploadReceipt{Artifact: meta.Ref})
}

func (s *Server) uploadReceipt(ctx context.Context, client string, r *pb.ArtifactChunk) (*pb.ArtifactRef, error) {
	var id string
	err := s.Pool.QueryRow(ctx, `SELECT a.id::text FROM artifacts a JOIN tasks t ON t.id=a.uploaded_by_task WHERE t.id=$1 AND t.client_id=$2 AND t.lease_id=$3 AND t.lease_token_hash=$4 AND a.upload_id=$5`, r.TaskId, client, r.LeaseId, identity.Hash(r.LeaseToken), r.UploadId).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	meta, err := artifact.Read(ctx, s.Pool, id)
	if err != nil {
		return nil, err
	}
	if meta.Ref.Sha256Hex != r.ExpectedSha256Hex || meta.Ref.SizeBytes != r.TotalSizeBytes || meta.Ref.ModelSchemaHash != r.ModelSchemaHash || meta.Ref.Codec != r.Codec {
		return nil, errors.New("IDEMPOTENCY_CONFLICT")
	}
	return meta.Ref, nil
}
func (s *Server) downloadMeta(ctx context.Context, client string, r *pb.DownloadRequest) (artifact.Metadata, error) {
	var meta artifact.Metadata
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		lease, err := scheduler.Lock(ctx, tx, client, r.TaskId, r.LeaseId, r.LeaseToken)
		if err != nil {
			return err
		}
		version := lease.Base
		if lease.Kind == "evaluate" {
			version = lease.Round
		}
		var allowed bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_versions WHERE run_id=$1 AND version_no=$2 AND artifact_id=$3)", lease.RunID, version, r.ArtifactId).Scan(&allowed)
		if err != nil {
			return err
		}
		if !allowed {
			return errors.New("FORBIDDEN")
		}
		meta, err = artifact.Read(ctx, tx, r.ArtifactId)
		if err != nil {
			return err
		}
		if meta.Ref.Sha256Hex != r.ExpectedSha256Hex {
			return errors.New("INCOMPATIBLE_MODEL")
		}
		return nil
	})
	return meta, err
}
func (s *Server) Download(r *pb.DownloadRequest, stream grpc.ServerStreamingServer[pb.ArtifactChunk]) error {
	ctx := stream.Context()
	client, err := s.client(ctx, "")
	if err != nil {
		return rpcError(err)
	}
	meta, err := s.downloadMeta(ctx, client, r)
	if err != nil {
		return rpcError(err)
	}
	data, err := s.Blobs.Get(ctx, meta.Key, meta.Ref.Sha256Hex, int64(meta.Ref.SizeBytes))
	if err != nil {
		return rpcError(err)
	}
	for offset := 0; offset < len(data); offset += 1 << 20 {
		end := min(offset+1<<20, len(data))
		if err = stream.Send(&pb.ArtifactChunk{Offset: uint64(offset), Data: data[offset:end], ExpectedSha256Hex: meta.Ref.Sha256Hex, Codec: meta.Ref.Codec, ModelSchemaHash: meta.Ref.ModelSchemaHash, TotalSizeBytes: meta.Ref.SizeBytes}); err != nil {
			return err
		}
	}
	return nil
}
