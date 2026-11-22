package adapters

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
	"strings"
	"time"
)

func Scope(ctx context.Context, q db.Query, user, kind, id string) error {
	acl, err := accessSchema(ctx, q)
	if err != nil {
		return err
	}
	return legacyScope(ctx, q, user, kind, id, acl)
}
func (s Store) Detail(ctx context.Context, access Access, kind, id string) (json.RawMessage, error) {
	var result json.RawMessage
	err := s.protected(ctx, access, func(ctx context.Context, tx pgx.Tx, user identity.User, acl bool) error {
		if kind != "runs" && kind != "experiments" {
			return Failure{404, "NOT_FOUND"}
		}
		if err := checkProject(ctx, tx, access, user, acl, strings.TrimSuffix(kind, "s"), id, projectaccess.Read); err != nil {
			return err
		}
		if kind == "runs" {
			var err error
			result, err = Run(ctx, tx, id)
			return err
		}
		return tx.QueryRow(ctx, `SELECT json_build_object('id',id,'project_id',project_id,'name',name,'config',config_json,'config_hash',config_hash,'created_at',created_at) FROM experiments WHERE id=$1`, id).Scan(&result)
	})
	if err != nil {
		return nil, err
	}
	return result, err
}
func (s Store) List(ctx context.Context, access Access, kind, id, filter string, cut time.Time, offset, limit int) ([]json.RawMessage, error) {
	var result []json.RawMessage
	err := s.protected(ctx, access, func(ctx context.Context, tx pgx.Tx, user identity.User, acl bool) error {
		var err error
		result, err = listTx(ctx, tx, access, user, acl, kind, id, filter, cut, offset, limit)
		return err
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
func listTx(ctx context.Context, tx pgx.Tx, access Access, user identity.User, acl bool, kind, id, filter string, cut time.Time, offset, limit int) ([]json.RawMessage, error) {
	var query string
	switch kind {
	case "projects":
		if access.OrganizationID != "" {
			return nil, Failure{404, "NOT_FOUND"}
		}
		mode := ""
		if acl {
			mode = " AND access_mode='legacy_private'"
		}
		query = `SELECT json_build_object('id',id,'owner_id',owner_id,'slug',slug,'name',name,'created_at',created_at) FROM projects WHERE owner_id=$1 AND created_at<=$2` + mode + ` ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`
	case "experiments":
		query = `SELECT json_build_object('id',id,'project_id',project_id,'name',name,'config',config_json,'config_hash',config_hash,'created_at',created_at) FROM experiments WHERE project_id=$1 AND created_at<=$2 ORDER BY created_at DESC,id DESC LIMIT $3 OFFSET $4`
	case "runs":
		query = `SELECT ` + runJSON + ` FROM runs r JOIN experiments e ON e.id=r.experiment_id WHERE e.project_id=$1 AND r.requested_at<=$2 AND ($5='' OR r.status=$5) ORDER BY r.requested_at DESC,r.id DESC LIMIT $3 OFFSET $4`
	case "rounds":
		query = `SELECT json_build_object('id',ro.id,'run_id',ro.run_id,'round_no',ro.round_no,'status',ro.status,'selected_count',ro.selected_count,'accepted_count',ro.accepted_count,'model_version',(SELECT version_no FROM model_versions WHERE produced_by_round_id=ro.id)) FROM rounds ro WHERE run_id=$1 AND started_at<=$2 ORDER BY round_no,id LIMIT $3 OFFSET $4`
	case "metrics":
		query = `SELECT json_build_object('id',m.id::text,'run_id',m.run_id,'round_no',ro.round_no,'split',m.split,'name',m.metric_name,'value',m.metric_value,'measured_at',m.measured_at,'client_id',m.client_id) FROM metric_points m LEFT JOIN rounds ro ON ro.id=m.round_id WHERE m.run_id=$1 AND measured_at<=$2 AND ($5='' OR m.split=$5) ORDER BY m.id LIMIT $3 OFFSET $4`
	case "events":
		query = `SELECT json_build_object('sequence',id::text,'run_id',run_id,'type',event_type,'created_at',created_at) FROM audit_events WHERE run_id=$1 AND created_at<=$2 ORDER BY id LIMIT $3 OFFSET $4`
	default:
		return nil, Failure{404, "NOT_FOUND"}
	}
	args := []any{id, cut, limit, offset}
	if kind == "projects" {
		args[0] = user.ID
	} else {
		scope := "runs"
		if kind == "experiments" || kind == "runs" {
			scope = "projects"
		}
		if err := checkProject(ctx, tx, access, user, acl, strings.TrimSuffix(scope, "s"), id, projectaccess.Read); err != nil {
			return nil, err
		}
	}
	if kind == "runs" || kind == "metrics" {
		args = append(args, filter)
	}
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, rows.Err()
}
func (s Store) Presets(ctx context.Context) ([]json.RawMessage, error) {
	rows, err := s.Pool.Query(ctx, "SELECT json_build_object('id',id,'config',config_json) FROM model_presets ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []json.RawMessage{}
	for rows.Next() {
		var raw json.RawMessage
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		result = append(result, raw)
	}
	return result, rows.Err()
}
