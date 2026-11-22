package inference

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Failure struct {
	Status int
	Code   string
}

func (e Failure) Error() string { return e.Code }

type Store struct {
	Pool   *pgxpool.Pool
	Keys   Keyring
	Policy Policy
	client *http.Client
}

func New(pool *pgxpool.Pool, keys Keyring, allowed []string) *Store {
	return &Store{Pool: pool, Keys: keys, Policy: Policy{Allowed: allowed}, client: newClient()}
}

type Provider struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	BaseURL   string    `json:"base_url"`
	Enabled   bool      `json:"enabled"`
	Revision  int       `json:"revision"`
	HasKey    bool      `json:"has_key"`
	UpdatedAt time.Time `json:"updated_at"`
}
type Input struct {
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Enabled  bool   `json:"enabled"`
	Revision int    `json:"revision"`
}

func (s *Store) List(ctx context.Context, owner string) ([]Provider, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,name,base_url,enabled,revision,updated_at FROM ai_providers WHERE owner_id=$1 ORDER BY created_at,id`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []Provider{}
	for rows.Next() {
		var p Provider
		p.HasKey = true
		if err = rows.Scan(&p.ID, &p.Name, &p.BaseURL, &p.Enabled, &p.Revision, &p.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

func validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" || utf8.RuneCountInString(in.Name) > 80 || in.Revision < 0 || in.Revision > 2147483646 || len(in.APIKey) > 4096 {
		return Failure{400, "INVALID_REQUEST"}
	}
	if strings.IndexFunc(in.Name, unicode.IsControl) >= 0 || strings.IndexFunc(in.APIKey, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return Failure{400, "INVALID_REQUEST"}
	}
	return nil
}

// Revision 0 creates; subsequent updates require the last observed revision.
// Owner-row locking serializes the per-user limit across API replicas.
func (s *Store) Save(ctx context.Context, owner, id string, in Input) (Provider, error) {
	var out Provider
	if err := validate(in); err != nil {
		return out, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&locked); err != nil {
		return out, err
	}
	var old Provider
	var ciphertext []byte
	var keyVersion string
	err = tx.QueryRow(ctx, `SELECT id,name,base_url,enabled,revision,updated_at,encrypted_secret,key_version FROM ai_providers WHERE id=$1 AND owner_id=$2 FOR UPDATE`, id, owner).Scan(&old.ID, &old.Name, &old.BaseURL, &old.Enabled, &old.Revision, &old.UpdatedAt, &ciphertext, &keyVersion)
	creating := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !creating {
		return out, err
	}
	if creating && in.Revision != 0 {
		return out, Failure{404, "NOT_FOUND"}
	}
	if !creating && in.Revision != old.Revision {
		return out, Failure{409, "VERSION_CONFLICT"}
	}
	// Address is immutable: changing destination requires a new connection.
	if !creating && in.BaseURL != old.BaseURL {
		return out, Failure{400, "PROVIDER_URL_IMMUTABLE"}
	}
	if creating {
		var count int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM ai_providers WHERE owner_id=$1`, owner).Scan(&count); err != nil {
			return out, err
		}
		if count >= 20 {
			return out, Failure{409, "PROVIDER_LIMIT"}
		}
		if in.APIKey == "" {
			return out, Failure{400, "API_KEY_REQUIRED"}
		}
	}
	// Disabled connections can still be managed after removal from the allowlist.
	if creating || in.Enabled {
		in.BaseURL, err = s.Policy.Validate(in.BaseURL)
		if err != nil {
			return out, err
		}
	}
	if in.APIKey != "" {
		ciphertext, keyVersion, err = s.Keys.Seal(owner, id, in.APIKey)
		if err != nil {
			return out, err
		}
	}
	if creating {
		tag, e := tx.Exec(ctx, `INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version,enabled) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(id) DO NOTHING`, id, owner, strings.TrimSpace(in.Name), in.BaseURL, ciphertext, keyVersion, in.Enabled)
		if e != nil {
			return out, e
		}
		if tag.RowsAffected() != 1 {
			return out, Failure{409, "VERSION_CONFLICT"}
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE ai_providers SET name=$3,encrypted_secret=$4,key_version=$5,enabled=$6,revision=revision+1,updated_at=now() WHERE id=$1 AND owner_id=$2`, id, owner, strings.TrimSpace(in.Name), ciphertext, keyVersion, in.Enabled)
		if err != nil {
			return out, err
		}
	}
	out.HasKey = true
	err = tx.QueryRow(ctx, `SELECT id,name,base_url,enabled,revision,updated_at FROM ai_providers WHERE id=$1 AND owner_id=$2`, id, owner).Scan(&out.ID, &out.Name, &out.BaseURL, &out.Enabled, &out.Revision, &out.UpdatedAt)
	if err != nil {
		return out, err
	}
	action := "ai.provider.update"
	if creating {
		action = "ai.provider.create"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO administration_events(actor_id,target_id,action) VALUES($1,$2,$3)`, owner, id, action); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
