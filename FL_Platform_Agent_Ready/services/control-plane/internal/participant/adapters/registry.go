package adapters

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/config"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

type Registry struct {
	Pool *pgxpool.Pool
	Keys [5]string
}

func New(pool *pgxpool.Pool) (*Registry, error) {
	r := &Registry{Pool: pool}
	for i := range r.Keys {
		key, err := config.Secret(fmt.Sprintf("client_%d", i))
		if err != nil {
			return nil, err
		}
		r.Keys[i] = identity.Hash(key)
	}
	return r, nil
}
func (r *Registry) Shard(token string) (int, error) {
	hash := identity.Hash(token)
	for i, key := range r.Keys {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(key)) == 1 {
			return i, nil
		}
	}
	return 0, errors.New("UNAUTHORIZED")
}
func (r *Registry) Register(ctx context.Context, shard int, request *pb.RegisterRequest) (string, error) {
	if request.ProtocolVersion != 1 || request.Capabilities == nil || request.Capabilities.Runtime != "python-pytorch" || len(request.DisplayName) > 120 {
		return "", errors.New("INVALID_CONFIG")
	}
	caps, err := json.Marshal(request.Capabilities)
	if err != nil {
		return "", err
	}
	var id string
	err = db.Transaction(ctx, r.Pool, func(tx pgx.Tx) error {
		// Both identity and shard are unique: serialize first registration across replicas.
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(726182903,$1)", shard); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO clients(display_name,identity_fingerprint,software_version,capabilities_json,last_seen_at,status,shard_index) VALUES($1,$2,$3,$4,now(),'online',$5) ON CONFLICT(identity_fingerprint) DO UPDATE SET last_seen_at=now(),software_version=EXCLUDED.software_version,status=CASE WHEN clients.status='disabled' THEN 'disabled' ELSE 'online' END RETURNING id::text`, request.DisplayName, r.Keys[shard], request.Capabilities.SdkVersion, caps, shard).Scan(&id)
	})
	return id, err
}
func (r *Registry) Client(ctx context.Context, shard int) (string, error) {
	var id string
	err := r.Pool.QueryRow(ctx, `SELECT id::text FROM clients WHERE identity_fingerprint=$1 AND status<>'disabled'`, r.Keys[shard]).Scan(&id)
	return id, err
}
