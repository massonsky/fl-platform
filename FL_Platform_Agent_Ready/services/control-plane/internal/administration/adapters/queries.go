package adapters

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func actorLookupFailure(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return Failure{401, "UNAUTHORIZED"}
	}
	return Failure{503, "DEPENDENCY_UNAVAILABLE"}
}

// Administration reads remain admin-only even if role changed after HTTP authentication.
func (s Store) Page(ctx context.Context, actor, kind string, offset, limit int) ([]json.RawMessage, bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, false, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	defer tx.Rollback(ctx)
	if err = authorization.Read(ctx, tx); err != nil {
		return nil, false, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	items, more, err := pageTx(ctx, tx, actor, kind, offset, limit)
	if err != nil {
		return nil, false, err
	}
	return items, more, tx.Commit(ctx)
}

// pageTx requires the caller to acquire authorization.Read/Write first.
func pageTx(ctx context.Context, tx pgx.Tx, actor, kind string, offset, limit int) ([]json.RawMessage, bool, error) {
	var role string
	if err := tx.QueryRow(ctx, "SELECT role FROM users WHERE id=$1 AND disabled_at IS NULL", actor).Scan(&role); err != nil {
		return nil, false, actorLookupFailure(err)
	}
	if role != "admin" {
		return nil, false, Failure{403, "FORBIDDEN"}
	}
	var query string
	switch kind {
	case "users":
		query = `SELECT json_build_object('id',id,'username',external_subject,'name',display_name,'role',role,'disabled',disabled_at IS NOT NULL,'created_at',created_at) FROM users ORDER BY created_at,id LIMIT $1 OFFSET $2`
	case "agents":
		query = `SELECT json_build_object('id',c.id,'name',c.display_name,'shard',c.shard_index,'version',c.software_version,'status',CASE WHEN c.status='disabled' THEN 'disabled' WHEN c.last_seen_at>now()-interval '30 seconds' THEN 'online' ELSE 'offline' END,'last_seen_at',c.last_seen_at,'active_tasks',(SELECT count(*) FROM tasks t WHERE t.client_id=c.id AND t.status='leased' AND t.lease_until>now())) FROM clients c ORDER BY c.shard_index,c.id LIMIT $1 OFFSET $2`
	case "events":
		query = `SELECT json_build_object('id',e.id,'actor',u.display_name,'target_id',e.target_id,'action',e.action,'created_at',e.created_at) FROM administration_events e JOIN users u ON u.id=e.actor_id ORDER BY e.created_at DESC,e.id LIMIT $1 OFFSET $2`
	default:
		return nil, false, Failure{404, "NOT_FOUND"}
	}
	rows, err := tx.Query(ctx, query, limit+1, offset)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	items := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return nil, false, err
		}
		items = append(items, json.RawMessage(raw))
	}
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(items) > limit
	if more {
		items = items[:limit]
	}
	return items, more, nil
}
