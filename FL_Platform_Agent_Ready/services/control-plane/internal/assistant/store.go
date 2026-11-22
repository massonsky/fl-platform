package assistant

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

type Store struct{ Pool *pgxpool.Pool }
type Input struct {
	Name        string `json:"name"`
	ProviderID  string `json:"provider_id"`
	Model       string `json:"model"`
	Instruction string `json:"instruction"`
	Enabled     bool   `json:"enabled"`
	Revision    int    `json:"revision"`
}
type Profile struct {
	ID string `json:"id"`
	Input
	UpdatedAt time.Time `json:"updated_at"`
}

const columns = `id,name,provider_id,model,instruction,enabled,revision,updated_at`

func scan(row pgx.Row) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.Name, &p.ProviderID, &p.Model, &p.Instruction, &p.Enabled, &p.Revision, &p.UpdatedAt)
	return p, err
}
func failure(status int, code string) error { return inference.Failure{Status: status, Code: code} }
func validate(in Input) error {
	if _, err := uuid.Parse(in.ProviderID); err != nil {
		return failure(400, "INVALID_REQUEST")
	}
	if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || !utf8.ValidString(in.Name) || strings.IndexFunc(in.Name, unicode.IsControl) >= 0 {
		return failure(400, "INVALID_REQUEST")
	}
	if strings.TrimSpace(in.Model) == "" || len(in.Model) > 256 || !utf8.ValidString(in.Model) || strings.IndexFunc(in.Model, unicode.IsControl) >= 0 {
		return failure(400, "INVALID_REQUEST")
	}
	if strings.TrimSpace(in.Instruction) == "" || len(in.Instruction) > 16384 || !utf8.ValidString(in.Instruction) || strings.ContainsRune(in.Instruction, 0) || in.Revision < 0 {
		return failure(400, "INVALID_REQUEST")
	}
	return nil
}
func (s Store) List(ctx context.Context, owner string) ([]Profile, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+columns+` FROM ai_assistants WHERE owner_id=$1 ORDER BY updated_at DESC,id LIMIT 50`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Profile{}
	for rows.Next() {
		p, e := scan(rows)
		if e != nil {
			return nil, e
		}
		items = append(items, p)
	}
	return items, rows.Err()
}
func (s Store) Save(ctx context.Context, owner, id string, in Input) (Profile, error) {
	var p Profile
	if err := validate(in); err != nil {
		return p, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return p, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&locked); err != nil {
		return p, err
	}
	var current int
	err = tx.QueryRow(ctx, `SELECT revision FROM ai_assistants WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		if in.Revision != 0 {
			return p, failure(404, "NOT_FOUND")
		}
	} else if err != nil {
		return p, err
	} else if current != in.Revision {
		return p, failure(409, "VERSION_CONFLICT")
	}
	if err = tx.QueryRow(ctx, `SELECT id FROM ai_providers WHERE id=$1 AND owner_id=$2 FOR SHARE`, in.ProviderID, owner).Scan(&locked); errors.Is(err, pgx.ErrNoRows) {
		return p, failure(404, "NOT_FOUND")
	} else if err != nil {
		return p, err
	}
	if current == 0 {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM ai_assistants WHERE owner_id=$1`, owner).Scan(&count); err != nil {
			return p, err
		}
		if count >= 50 {
			return p, failure(409, "ASSISTANT_LIMIT")
		}
		p, err = scan(tx.QueryRow(ctx, `INSERT INTO ai_assistants(id,owner_id,name,provider_id,model,instruction,enabled) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING RETURNING `+columns, id, owner, in.Name, in.ProviderID, in.Model, in.Instruction, in.Enabled))
		if errors.Is(err, pgx.ErrNoRows) {
			return p, failure(409, "VERSION_CONFLICT")
		}
	} else {
		p, err = scan(tx.QueryRow(ctx, `UPDATE ai_assistants SET name=$3,provider_id=$4,model=$5,instruction=$6,enabled=$7,revision=revision+1,updated_at=now() WHERE id=$1 AND owner_id=$2 RETURNING `+columns, id, owner, in.Name, in.ProviderID, in.Model, in.Instruction, in.Enabled))
	}
	if err != nil {
		return p, err
	}
	return p, tx.Commit(ctx)
}
