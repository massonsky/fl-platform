package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type Usage struct {
	Prompt     *int64 `json:"prompt_tokens,omitempty"`
	Completion *int64 `json:"completion_tokens,omitempty"`
	Total      *int64 `json:"total_tokens,omitempty"`
}

func (s *Store) Chat(ctx context.Context, owner, provider string, revision int, model string, messages []ChatMessage, emit func(string) error) (string, *Usage, error) {
	var base, version string
	var encrypted []byte
	var enabled bool
	var current int
	err := s.Pool.QueryRow(ctx, `SELECT base_url,key_version,encrypted_secret,enabled,revision FROM ai_providers WHERE id=$1 AND owner_id=$2`, provider, owner).Scan(&base, &version, &encrypted, &enabled, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, Failure{404, "NOT_FOUND"}
	}
	if err != nil {
		return "", nil, err
	}
	if !enabled {
		return "", nil, Failure{409, "PROVIDER_DISABLED"}
	}
	if current != revision {
		return "", nil, Failure{409, "PROVIDER_CHANGED"}
	}
	if _, err = s.Policy.Validate(base); err != nil {
		return "", nil, err
	}
	secret, err := s.Keys.Open(owner, provider, version, encrypted)
	if err != nil {
		return "", nil, errors.New("AI secret unavailable")
	}
	client := *s.client
	client.Timeout = 120 * time.Second
	return fetchChat(ctx, &client, base, secret, model, messages, emit)
}

func fetchChat(ctx context.Context, client *http.Client, base, secret, model string, messages []ChatMessage, emit func(string) error) (string, *Usage, error) {
	raw, err := json.Marshal(struct {
		Model     string        `json:"model"`
		Messages  []ChatMessage `json:"messages"`
		Stream    bool          `json:"stream"`
		MaxTokens int           `json:"max_tokens"`
	}{model, messages, true, 1024})
	if err != nil {
		return "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	res, err := client.Do(req)
	if err != nil {
		return "", nil, Failure{502, "PROVIDER_UNAVAILABLE"}
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		code := "PROVIDER_UNAVAILABLE"
		switch res.StatusCode {
		case 401, 403:
			code = "PROVIDER_AUTH_FAILED"
		case 429:
			code = "PROVIDER_RATE_LIMITED"
		case 400, 404, 422:
			code = "MODEL_REQUEST_REJECTED"
		}
		return "", nil, Failure{502, code}
	}
	media, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "text/event-stream" {
		return "", nil, Failure{502, "INVALID_PROVIDER_RESPONSE"}
	}
	return readChatStream(res.Body, emit)
}
