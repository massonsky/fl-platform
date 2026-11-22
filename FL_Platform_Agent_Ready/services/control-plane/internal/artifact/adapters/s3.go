package adapters

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/config"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
)

type Store struct {
	client *minio.Client
	bucket string
}

func Open() (*Store, error) {
	user, err := config.Secret("minio_user")
	if err != nil {
		return nil, err
	}
	password, err := config.Secret("minio_password")
	if err != nil {
		return nil, err
	}
	client, err := minio.New(config.Env("FL_S3_ENDPOINT", "minio:9000"), &minio.Options{Creds: credentials.NewStaticV4(user, password, ""), Secure: false})
	return &Store{client: client, bucket: config.Env("FL_S3_BUCKET", "flrp-artifacts")}, err
}
func Hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Ready checks credentials and the configured bucket without creating objects.
// It does not certify write permissions or integrity of every stored artifact.
func (s *Store) Ready(ctx context.Context) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("artifact store not configured")
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("artifact bucket unavailable")
	}
	return nil
}

func (s *Store) Put(ctx context.Context, data []byte) (string, error) {
	key := "models/" + uuid.NewString() + "/" + Hash(data)
	// A fresh key per upload prevents overwriting any previous artifact.
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return key, err
}
func (s *Store) Get(ctx context.Context, key, hash string, size int64) ([]byte, error) {
	if size <= 0 || size > 256<<20 {
		return nil, fmt.Errorf("invalid object size")
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer object.Close()
	data, err := io.ReadAll(io.LimitReader(object, size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size || Hash(data) != hash {
		return nil, fmt.Errorf("artifact integrity failure")
	}
	return data, nil
}
