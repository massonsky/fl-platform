package inference

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

type Model struct {
	ID string `json:"id"`
}

// Models checks catalog reachability without sending prompts or performing inference.
// A public catalog does not prove credential validity or permission to generate.
// Returned IDs make no assertion about chat/vision/training capabilities.
func (s *Store) Models(ctx context.Context, owner, id string) ([]Model, error) {
	var base, version string
	var encrypted []byte
	var enabled bool
	err := s.Pool.QueryRow(ctx, `SELECT base_url,key_version,encrypted_secret,enabled FROM ai_providers WHERE id=$1 AND owner_id=$2`, id, owner).Scan(&base, &version, &encrypted, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, Failure{404, "NOT_FOUND"}
	}
	if err != nil {
		return nil, err
	}
	if !enabled {
		return nil, Failure{409, "PROVIDER_DISABLED"}
	}
	if _, err = s.Policy.Validate(base); err != nil {
		return nil, err
	}
	// Durable throttling shared by API replicas; no unbounded outbound probes.
	tag, err := s.Pool.Exec(ctx, `UPDATE ai_providers SET last_probe_at=now() WHERE id=$1 AND owner_id=$2 AND (last_probe_at IS NULL OR last_probe_at < now()-interval '10 seconds')`, id, owner)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, Failure{429, "RATE_LIMITED"}
	}
	secret, err := s.Keys.Open(owner, id, version, encrypted)
	if err != nil {
		return nil, errors.New("AI secret unavailable")
	}
	return fetchModels(ctx, s.client, base, secret)
}

func fetchModels(ctx context.Context, client *http.Client, base, secret string) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/models", nil)
	if err != nil {
		return nil, Failure{400, "INVALID_PROVIDER_URL"}
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return nil, Failure{502, "PROVIDER_UNAVAILABLE"}
	}
	defer res.Body.Close()
	switch res.StatusCode {
	case 401, 403:
		return nil, Failure{502, "PROVIDER_AUTH_FAILED"}
	case 429:
		return nil, Failure{429, "PROVIDER_RATE_LIMITED"}
	case 200:
	default:
		return nil, Failure{502, "PROVIDER_UNAVAILABLE"}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, Failure{502, "INVALID_PROVIDER_RESPONSE"}
	}
	var response struct {
		Data []Model `json:"data"`
	}
	if json.Unmarshal(data, &response) != nil || response.Data == nil || len(response.Data) > 1000 {
		return nil, Failure{502, "INVALID_PROVIDER_RESPONSE"}
	}
	seen := map[string]bool{}
	items := []Model{}
	for _, m := range response.Data {
		if m.ID == "" || len(m.ID) > 256 || strings.ContainsAny(m.ID, "\r\n\x00") {
			return nil, Failure{502, "INVALID_PROVIDER_RESPONSE"}
		}
		if !seen[m.ID] {
			items = append(items, m)
			seen[m.ID] = true
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}
