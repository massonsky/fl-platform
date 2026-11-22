package adapters

import (
	"context"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

type Metadata struct {
	Ref *pb.ArtifactRef
	Key string
}

func Read(ctx context.Context, q db.Query, id string) (Metadata, error) {
	meta := Metadata{Ref: &pb.ArtifactRef{SchemaVersion: 1}}
	err := q.QueryRow(ctx, `SELECT id::text,storage_key,sha256_hex,size_bytes,codec,model_schema_hash FROM artifacts WHERE id=$1 AND state<>'deleted'`, id).Scan(&meta.Ref.ArtifactId, &meta.Key, &meta.Ref.Sha256Hex, &meta.Ref.SizeBytes, &meta.Ref.Codec, &meta.Ref.ModelSchemaHash)
	return meta, err
}
func Record(ctx context.Context, q db.Query, key, hash, schema string, size int, task, upload *string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `INSERT INTO artifacts(storage_key,sha256_hex,size_bytes,codec,model_schema_hash,state,uploaded_by_task,upload_id) VALUES($1,$2,$3,'FLOAT32_LE',$4,'committed',$5,$6) ON CONFLICT(storage_key) DO UPDATE SET storage_key=excluded.storage_key RETURNING id::text`, key, hash, size, schema, task, upload).Scan(&id)
	return id, err
}
