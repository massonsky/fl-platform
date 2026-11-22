package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	jcs "github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	config "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/domain"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Store struct {
	Pool  *pgxpool.Pool
	Blobs ExportBlobs
}
type Result struct {
	Status int
	Body   json.RawMessage
}
type Failure struct {
	Status int
	Code   string
}

func (f Failure) Error() string { return f.Code }
func Canonical(raw []byte) ([]byte, error) {
	if !utf8.Valid(raw) {
		return nil, Failure{400, "INVALID_REQUEST"}
	}
	data, err := jcs.Transform(raw)
	if err != nil {
		return nil, Failure{400, "INVALID_REQUEST"}
	}
	return data, nil
}
func Decode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return Failure{400, "INVALID_REQUEST"}
	}
	return nil
}
func (s Store) Mutate(ctx context.Context, access Access, operation, id, key string, raw []byte) (Result, error) {
	var result Result
	if !regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`).MatchString(key) {
		return result, Failure{400, "INVALID_REQUEST"}
	}
	canonical := []byte("{}")
	var err error
	if operation == "project" || operation == "experiment" {
		canonical, err = Canonical(raw)
		if err != nil {
			return result, err
		}
	} else if len(raw) > 0 {
		return result, Failure{400, "INVALID_REQUEST"}
	}
	scope := operation + "/" + id
	if access.OrganizationID != "" {
		scope = access.OrganizationID + "/" + scope
	}
	fingerprint := artifact.Hash(append([]byte(scope+"\n"), canonical...))
	err = s.protected(ctx, access, func(ctx context.Context, tx pgx.Tx, user identity.User, acl bool) error {
		if err := authorizeMutation(ctx, tx, access, user, acl, operation, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", user.ID+"/"+operation+"/"+key); err != nil {
			return err
		}
		if _, err := identity.Current(ctx, tx, access.Principal); err != nil {
			return err
		}
		var old string
		err := tx.QueryRow(ctx, "SELECT fingerprint,response_status,response_body FROM http_receipts WHERE user_id=$1 AND operation=$2 AND idempotency_key=$3", user.ID, operation, key).Scan(&old, &result.Status, &result.Body)
		if err == nil {
			if old != fingerprint {
				return Failure{409, "IDEMPOTENCY_CONFLICT"}
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		switch operation {
		case "project":
			result, err = createProject(ctx, tx, user, canonical)
		case "experiment":
			result, err = createExperiment(ctx, tx, id, canonical)
		case "queue":
			result, err = queueRun(ctx, tx, id, user.ID)
		case "cancel":
			result, err = cancelRun(ctx, tx, id)
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO http_receipts(user_id,operation,idempotency_key,fingerprint,response_status,response_body) VALUES($1,$2,$3,$4,$5,$6)", user.ID, operation, key, fingerprint, result.Status, result.Body)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}
func createProject(ctx context.Context, tx pgx.Tx, user identity.User, raw []byte) (Result, error) {
	var input struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	result := Result{Status: 201}
	if err := Decode(raw, &input); err != nil {
		return result, err
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}$`).MatchString(input.Slug) || strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 120 {
		return result, Failure{400, "INVALID_REQUEST"}
	}
	err := tx.QueryRow(ctx, `INSERT INTO projects(owner_id,slug,name) VALUES($1,$2,$3) RETURNING json_build_object('id',id,'owner_id',owner_id,'slug',slug,'name',name,'created_at',created_at)`, user.ID, input.Slug, input.Name).Scan(&result.Body)
	return result, err
}
func createExperiment(ctx context.Context, tx pgx.Tx, project string, raw []byte) (Result, error) {
	var input struct {
		Name   string          `json:"name"`
		Config json.RawMessage `json:"config"`
	}
	result := Result{Status: 201}
	if err := Decode(raw, &input); err != nil {
		return result, err
	}
	if strings.TrimSpace(input.Name) == "" || utf8.RuneCountInString(input.Name) > 120 {
		return result, Failure{400, "INVALID_REQUEST"}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input.Config, &fields); err != nil {
		return result, Failure{400, "INVALID_CONFIG"}
	}
	if len(fields) != 23 {
		return result, Failure{400, "INVALID_CONFIG"}
	}
	for _, value := range fields {
		if string(value) == "null" {
			return result, Failure{400, "INVALID_CONFIG"}
		}
	}
	var c config.Config
	if err := Decode(input.Config, &c); err != nil {
		return result, err
	}
	if c.Validate() != nil {
		return result, Failure{400, "INVALID_CONFIG"}
	}
	var policy map[string]json.RawMessage
	if json.Unmarshal(fields["completion_policy"], &policy) != nil || c.Completion.Mode == "strict" && len(policy) != 1 || c.Completion.Mode == "quorum" && len(policy) != 2 {
		return result, Failure{400, "INVALID_CONFIG"}
	}
	var compatible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_presets WHERE config_json->>'dataset_manifest_hash'=$1 AND config_json->>'partition_manifest_hash'=$2 AND config_json->>'model_schema_hash'=$3 AND config_json->>'initial_weights_hash'=$4 AND config_json->>'runtime_manifest_hash'=$5 AND config_json->>'seed'=$6 AND config_json->>'partition'=$7)`, c.DatasetHash, c.PartitionHash, c.SchemaHash, c.InitialHash, c.RuntimeHash, fieldsString(fields["seed"]), c.Partition).Scan(&compatible)
	if err != nil {
		return result, err
	}
	if !compatible {
		return result, Failure{400, "INVALID_CONFIG"}
	}
	canonical, err := Canonical(input.Config)
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO experiments(project_id,name,config_json,config_hash) VALUES($1,$2,$3,$4) RETURNING json_build_object('id',id,'project_id',project_id,'name',name,'config',config_json,'config_hash',config_hash,'created_at',created_at)`, project, input.Name, canonical, artifact.Hash(canonical)).Scan(&result.Body)
	return result, err
}
func fieldsString(raw json.RawMessage) string { return string(raw) }
