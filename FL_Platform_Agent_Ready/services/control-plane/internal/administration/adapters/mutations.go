package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

type Store struct{ Pool *pgxpool.Pool }
type Failure struct {
	Status int
	Code   string
}

func (f Failure) Error() string { return f.Code }

type Input struct {
	Username        string `json:"username,omitempty"`
	Name            string `json:"name,omitempty"`
	Role            string `json:"role,omitempty"`
	Password        string `json:"password,omitempty"`
	CurrentPassword string `json:"current_password,omitempty"`
	Disabled        *bool  `json:"disabled,omitempty"`
}

func ValidPassword(password string) bool {
	return utf8.ValidString(password) && len(password) >= 12 && len(password) <= 128 && strings.TrimSpace(password) == password
}
func validRole(role string) bool { return role == "admin" || role == "researcher" || role == "viewer" }
func Validate(op string, in Input) error {
	valid := false
	switch op {
	case "user.create":
		valid = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,63}$`).MatchString(in.Username) && len(strings.TrimSpace(in.Name)) > 0 && utf8.RuneCountInString(in.Name) <= 120 && validRole(in.Role) && ValidPassword(in.Password) && in.Disabled == nil && in.CurrentPassword == ""
	case "user.update":
		valid = validRole(in.Role) && in.Disabled != nil && in.Username == "" && in.Name == "" && in.CurrentPassword == "" && (in.Password == "" || ValidPassword(in.Password))
	case "agent.update":
		valid = in.Disabled != nil && in.Username == "" && in.Name == "" && in.Role == "" && in.Password == "" && in.CurrentPassword == ""
	case "password.change":
		valid = ValidPassword(in.Password) && len(in.CurrentPassword) > 0 && len(in.CurrentPassword) <= 128 && in.Username == "" && in.Name == "" && in.Role == "" && in.Disabled == nil
	}
	if !valid {
		return Failure{400, "INVALID_REQUEST"}
	}
	return nil
}
func (s Store) Mutate(ctx context.Context, actor, op, target, key, fingerprint string, in Input) (json.RawMessage, error) {
	if err := Validate(op, in); err != nil {
		return nil, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`).MatchString(key) {
		return nil, Failure{400, "INVALID_REQUEST"}
	}
	if credentialOperation(op, in) {
		return nil, Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	var output json.RawMessage
	err := db.Transaction(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := authorization.Write(ctx, tx); err != nil {
			return err
		}
		return mutateTx(ctx, tx, actor, op, target, key, fingerprint, in, &output)
	})
	return output, err
}

// mutateTx requires the caller to acquire authorization.Write first.
func mutateTx(ctx context.Context, tx pgx.Tx, actor, op, target, key, fingerprint string, in Input, output *json.RawMessage) error {
	// Actor-only/old Service entry points support noncredential writes only.
	return mutateWithPreparedTx(ctx, tx, actor, op, target, key, fingerprint, in, nil, output)
}

func mutateWithPreparedTx(ctx context.Context, tx pgx.Tx, actor, op, target, key, fingerprint string, in Input, prepared *preparedCredential, output *json.RawMessage) error {
	if credentialOperation(op, in) && prepared == nil {
		return Failure{503, "DEPENDENCY_UNAVAILABLE"}
	}
	var role string
	if err := tx.QueryRow(ctx, "SELECT role FROM users WHERE id=$1 AND disabled_at IS NULL FOR UPDATE", actor).Scan(&role); err != nil {
		return actorLookupFailure(err)
	}
	if op != "password.change" && role != "admin" {
		return Failure{403, "FORBIDDEN"}
	}
	var old string
	err := tx.QueryRow(ctx, "SELECT fingerprint,response FROM administration_receipts WHERE actor_id=$1 AND operation=$2 AND idempotency_key=$3", actor, op, key).Scan(&old, output)
	if err == nil {
		if old != fingerprint {
			return Failure{409, "IDEMPOTENCY_CONFLICT"}
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if prepared != nil {
		if err := prepared.snapshot.matches(ctx, tx, actor, op, target); err != nil {
			return err
		}
	}
	hash := ""
	if prepared != nil {
		hash = prepared.hash
	}
	switch op {
	case "user.create":
		target = uuid.NewString()
		_, err = tx.Exec(ctx, "INSERT INTO users(id,external_subject,display_name,role,password_hash) VALUES($1,$2,$3,$4,$5)", target, in.Username, strings.TrimSpace(in.Name), in.Role, hash)
	case "user.update":
		err = updateUser(ctx, tx, target, in, hash)
	case "password.change":
		target = actor
		_, err = tx.Exec(ctx, "UPDATE users SET password_hash=$2 WHERE id=$1", actor, hash)
	case "agent.update":
		var exists bool
		err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM clients WHERE id=$1)", target).Scan(&exists)
		if err == nil && !exists {
			return Failure{404, "NOT_FOUND"}
		}
		if err == nil {
			status := "offline"
			if *in.Disabled {
				status = "disabled"
			}
			_, err = tx.Exec(ctx, "UPDATE clients SET status=$2 WHERE id=$1", target, status)
		}
	}
	if err != nil {
		return err
	}
	if op == "user.update" || op == "password.change" {
		if _, err = tx.Exec(ctx, "DELETE FROM sessions WHERE user_id=$1", target); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "INSERT INTO administration_events(actor_id,target_id,action) VALUES($1,$2,$3)", actor, target, op); err != nil {
		return err
	}
	*output, _ = json.Marshal(map[string]any{"id": target, "saved": true})
	_, err = tx.Exec(ctx, "INSERT INTO administration_receipts(actor_id,operation,idempotency_key,fingerprint,response) VALUES($1,$2,$3,$4,$5)", actor, op, key, fingerprint, *output)
	return err
}
func updateUser(ctx context.Context, tx pgx.Tx, target string, in Input, hash string) error {
	var role string
	var disabled bool
	err := tx.QueryRow(ctx, "SELECT role,disabled_at IS NOT NULL FROM users WHERE id=$1 FOR UPDATE", target).Scan(&role, &disabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return Failure{404, "NOT_FOUND"}
	}
	if err != nil {
		return err
	}
	if role == "admin" && !disabled && (in.Role != "admin" || *in.Disabled) {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE role='admin' AND disabled_at IS NULL").Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return Failure{409, "LAST_ADMIN"}
		}
	}
	if !disabled && (role == "admin" || role == "researcher") && (*in.Disabled || in.Role == "viewer") {
		if err := organization.CheckIdentityRemoval(ctx, tx, target); err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, "UPDATE users SET role=$2,disabled_at=CASE WHEN $3 THEN coalesce(disabled_at,now()) ELSE NULL END,password_hash=CASE WHEN $4='' THEN password_hash ELSE $4 END WHERE id=$1", target, in.Role, *in.Disabled, hash)
	return err
}
