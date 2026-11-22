package legacylab

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

type Service struct{ Pool *pgxpool.Pool }

func (s Service) call(ctx context.Context, p identity.Principal, change bool, fn func(context.Context, pgx.Tx, identity.User) (Admission, error)) (Admission, error) {
	ctx, cancel := context.WithTimeout(ctx, authorization.OperationTimeout)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Admission{}, safeFailure(err)
	}
	defer tx.Rollback(ctx)
	if change {
		err = authorization.Write(ctx, tx)
	} else {
		err = authorization.Read(ctx, tx)
	}
	if err != nil {
		return Admission{}, safeFailure(err)
	}
	user, err := identity.Current(ctx, tx, p)
	if err != nil {
		return Admission{}, safeFailure(err)
	}
	if err = schemaReady(ctx, tx); err != nil {
		return Admission{}, safeFailure(err)
	}
	a, err := fn(ctx, tx, user)
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return Admission{}, safeFailure(err)
	}
	return a, nil
}

func (s Service) Self(ctx context.Context, p identity.Principal) (Admission, error) {
	return s.call(ctx, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (Admission, error) {
		return readAdmission(ctx, tx, user.ID, false)
	})
}

func (s Service) Get(ctx context.Context, p identity.Principal, target string) (Admission, error) {
	return s.call(ctx, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (Admission, error) {
		if user.Role != "admin" {
			return Admission{}, Failure{403, "FORBIDDEN"}
		}
		id, err := normalizedID(target)
		if err != nil {
			return Admission{}, err
		}
		return readAdmission(ctx, tx, id, false)
	})
}

func (s Service) Change(ctx context.Context, p identity.Principal, target string, in Change) (Admission, error) {
	return s.call(ctx, p, true, func(ctx context.Context, tx pgx.Tx, user identity.User) (Admission, error) {
		if user.Role != "admin" {
			return Admission{}, Failure{403, "FORBIDDEN"}
		}
		id, err := normalizedID(target)
		if err != nil {
			return Admission{}, err
		}
		input, err := validateChange(in)
		if err != nil {
			return Admission{}, err
		}
		old, err := readAdmission(ctx, tx, id, true)
		if err != nil {
			return Admission{}, err
		}
		if input.Revision != old.Revision {
			return Admission{}, Failure{409, "VERSION_CONFLICT"}
		}
		if old.Status == input.Status {
			return old, nil
		}
		_, err = tx.Exec(ctx, `INSERT INTO legacy_lab_admissions(user_id,status,source,updated_by)
 VALUES($1,$2,'admin',$3) ON CONFLICT(user_id) DO UPDATE SET status=EXCLUDED.status,
 source='admin',updated_by=EXCLUDED.updated_by,revision=legacy_lab_admissions.revision+1,updated_at=clock_timestamp()`, id, input.Status, user.ID)
		if err != nil {
			return Admission{}, err
		}
		updated, err := readAdmission(ctx, tx, id, false)
		if err != nil {
			return Admission{}, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO legacy_lab_admission_events(target_user_id,actor_id,action,before_json,after_json,reason)
 VALUES($1,$2,'changed',$3,$4,$5)`, id, user.ID, audit(old), audit(updated), input.Reason)
		return updated, err
	})
}

func audit(a Admission) []byte {
	raw, _ := json.Marshal(struct {
		Status   string `json:"status"`
		Revision int    `json:"revision"`
		Source   string `json:"source,omitempty"`
	}{a.Status, a.Revision, a.Source})
	return raw
}
