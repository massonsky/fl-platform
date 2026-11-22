package legacylab

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

type Failure struct {
	Status int
	Code   string
}

func (e Failure) Error() string { return e.Code }

type Admission struct {
	UserID              string     `json:"user_id"`
	Status              string     `json:"status"`
	Revision            int        `json:"revision"`
	Source              string     `json:"source,omitempty"`
	UpdatedAt           *time.Time `json:"updated_at,omitempty"`
	CanCreateLegacyWork bool       `json:"can_create_legacy_work"`
}

type Change struct {
	Status   string
	Revision int
	Reason   string
}

func safeFailure(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, identity.ErrUnauthorized) {
		return Failure{401, "UNAUTHORIZED"}
	}
	var f Failure
	if errors.As(err, &f) {
		return err
	}
	return Failure{503, "DEPENDENCY_UNAVAILABLE"}
}

func normalizedID(value string) (string, error) {
	id, err := uuid.Parse(value)
	if err != nil || id == uuid.Nil {
		return "", Failure{400, "INVALID_REQUEST"}
	}
	return id.String(), nil
}

func validateChange(in Change) (Change, error) {
	if (in.Status != "active" && in.Status != "revoked") || in.Revision < 0 || !utf8.ValidString(in.Reason) || strings.IndexFunc(in.Reason, unicode.IsControl) >= 0 {
		return in, Failure{400, "INVALID_REQUEST"}
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if in.Reason == "" || utf8.RuneCountInString(in.Reason) > 240 {
		return in, Failure{400, "INVALID_REQUEST"}
	}
	return in, nil
}

// Required tables/columns/types/nullability are checked even on Require paths
// that do not write audit. Full schema equivalence belongs to upgrade rehearsal;
// this bounded probe is not a verifier of every constraint or index definition.
func schemaReady(ctx context.Context, tx pgx.Tx) error {
	var ready bool
	err := tx.QueryRow(ctx, `SELECT bool_and(coalesce(c.relkind='r' AND a.atttypid=to_regtype(e.type_name)
 AND a.attnotnull=e.required AND NOT a.attisdropped,false)) FROM (VALUES
 ('legacy_lab_admissions','user_id','uuid',true),
 ('legacy_lab_admissions','status','text',true),
 ('legacy_lab_admissions','revision','integer',true),
 ('legacy_lab_admissions','source','text',true),
 ('legacy_lab_admissions','updated_by','uuid',false),
 ('legacy_lab_admissions','created_at','timestamptz',true),
 ('legacy_lab_admissions','updated_at','timestamptz',true),
 ('legacy_lab_admission_events','id','uuid',true),
 ('legacy_lab_admission_events','target_user_id','uuid',true),
 ('legacy_lab_admission_events','actor_id','uuid',false),
 ('legacy_lab_admission_events','action','text',true),
 ('legacy_lab_admission_events','before_json','jsonb',true),
 ('legacy_lab_admission_events','after_json','jsonb',true),
 ('legacy_lab_admission_events','reason','text',true),
 ('legacy_lab_admission_events','created_at','timestamptz',true)
 ) AS e(table_name,column_name,type_name,required)
 LEFT JOIN pg_class c ON c.relname=e.table_name AND c.relnamespace='public'::regnamespace
 LEFT JOIN pg_attribute a ON a.attrelid=c.oid AND a.attname=e.column_name`).Scan(&ready)
	if err != nil {
		return err
	}
	if !ready {
		return Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	return nil
}

func readAdmission(ctx context.Context, tx pgx.Tx, target string, lock bool) (Admission, error) {
	a := Admission{UserID: target, Status: "not_admitted"}
	var role string
	var disabled bool
	err := tx.QueryRow(ctx, "SELECT role,disabled_at IS NOT NULL FROM users WHERE id=$1", target).Scan(&role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Admission{}, Failure{404, "NOT_FOUND"}
	}
	if err != nil {
		return Admission{}, err
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	err = tx.QueryRow(ctx, "SELECT status,revision,source,updated_at FROM legacy_lab_admissions WHERE user_id=$1"+suffix, target).
		Scan(&a.Status, &a.Revision, &a.Source, &a.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, nil
	}
	if err != nil {
		return Admission{}, err
	}
	a.CanCreateLegacyWork = a.Status == "active" && !disabled && (role == "admin" || role == "researcher")
	return a, nil
}

// Require composes with an existing authorization.Read/Write + identity.Current
// transaction. Actor alone is not authentication. D1B will wire consuming routes.
func Require(ctx context.Context, tx pgx.Tx, actor identity.User) error {
	if err := schemaReady(ctx, tx); err != nil {
		return safeFailure(err)
	}
	id, err := normalizedID(actor.ID)
	if err != nil {
		return err
	}
	a, err := readAdmission(ctx, tx, id, false)
	if err != nil {
		return safeFailure(err)
	}
	if !a.CanCreateLegacyWork {
		return Failure{403, "LEGACY_LAB_NOT_ADMITTED"}
	}
	return nil
}
