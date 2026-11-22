package conversation

import (
	"context"
	"encoding/json"
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
type Conversation struct {
	AssistantID       string    `json:"assistant_id,omitempty"`
	AssistantRevision *int      `json:"assistant_revision,omitempty"`
	AssistantName     string    `json:"assistant_name,omitempty"`
	SystemInstruction string    `json:"system_instruction,omitempty"`
	ID                string    `json:"id"`
	ProviderID        string    `json:"provider_id"`
	ProviderName      string    `json:"provider_name"`
	BaseURL           string    `json:"base_url"`
	Model             string    `json:"model"`
	Title             string    `json:"title"`
	CreatedAt         time.Time `json:"created_at"`
}
type Generation struct {
	ID             string          `json:"request_id"`
	ConversationID string          `json:"conversation_id"`
	Input          string          `json:"input"`
	Content        string          `json:"content"`
	Status         string          `json:"status"`
	ErrorCode      string          `json:"error_code"`
	FinishReason   string          `json:"finish_reason"`
	Usage          json.RawMessage `json:"usage"`
	CreatedAt      time.Time       `json:"created_at"`
	Deadline       time.Time       `json:"deadline"`
}

const columns = `request_id,conversation_id,input,content,status,error_code,finish_reason,usage,created_at,deadline`

func scan(row pgx.Row) (Generation, error) {
	var g Generation
	err := row.Scan(&g.ID, &g.ConversationID, &g.Input, &g.Content, &g.Status, &g.ErrorCode, &g.FinishReason, &g.Usage, &g.CreatedAt, &g.Deadline)
	return g, err
}
func failure(status int, code string) error { return inference.Failure{Status: status, Code: code} }
func (s Store) Reap(ctx context.Context, owner string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE ai_generations SET status='interrupted',error_code='GENERATION_INTERRUPTED' WHERE owner_id=$1 AND status='streaming' AND deadline<=now()`, owner)
	return err
}
func (s Store) List(ctx context.Context, owner string) ([]Conversation, error) {
	if err := s.Reap(ctx, owner); err != nil {
		return nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.id,c.provider_id,p.name,p.base_url,c.model,c.title,c.created_at,COALESCE(c.assistant_id::text,''),c.assistant_revision,c.assistant_name,c.system_instruction FROM ai_conversations c JOIN ai_providers p ON p.id=c.provider_id WHERE c.owner_id=$1 AND c.deleted_at IS NULL ORDER BY c.created_at DESC,c.id LIMIT 100`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Conversation{}
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ID, &c.ProviderID, &c.ProviderName, &c.BaseURL, &c.Model, &c.Title, &c.CreatedAt, &c.AssistantID, &c.AssistantRevision, &c.AssistantName, &c.SystemInstruction); err != nil {
			return nil, err
		}
		items = append(items, c)
	}
	return items, rows.Err()
}
func (s Store) Create(ctx context.Context, owner, provider, model string, consent bool) (Conversation, error) {
	return s.CreateWithAssistant(ctx, owner, provider, model, consent, "", 0)
}
func (s Store) CreateWithAssistant(ctx context.Context, owner, provider, model string, consent bool, assistantID string, revision int) (Conversation, error) {
	var c Conversation
	if !consent {
		return c, failure(400, "CONSENT_REQUIRED")
	}
	if _, err := uuid.Parse(provider); err != nil || strings.TrimSpace(model) == "" || len(model) > 256 || !utf8.ValidString(model) || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return c, failure(400, "INVALID_REQUEST")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&locked); err != nil {
		return c, err
	}
	if assistantID != "" {
		if _, err = uuid.Parse(assistantID); err != nil {
			return c, failure(400, "INVALID_REQUEST")
		}
		var ap, am string
		var enabled bool
		var ar int
		err = tx.QueryRow(ctx, `SELECT id,revision,name,instruction,provider_id,model,enabled FROM ai_assistants WHERE id=$1 AND owner_id=$2 FOR SHARE`, assistantID, owner).Scan(&c.AssistantID, &ar, &c.AssistantName, &c.SystemInstruction, &ap, &am, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			return c, failure(404, "NOT_FOUND")
		}
		if err != nil {
			return c, err
		}
		if !enabled {
			return c, failure(409, "ASSISTANT_DISABLED")
		}
		if ar != revision {
			return c, failure(409, "VERSION_CONFLICT")
		}
		if provider != ap || model != am {
			return c, failure(400, "INVALID_REQUEST")
		}
		c.AssistantRevision = &ar
	} else if revision != 0 {
		return c, failure(400, "INVALID_REQUEST")
	}
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM ai_conversations WHERE owner_id=$1 AND deleted_at IS NULL`, owner).Scan(&count); err != nil {
		return c, err
	}
	if count >= 100 {
		return c, failure(409, "CONVERSATION_LIMIT")
	}
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT id,name,base_url,enabled FROM ai_providers WHERE id=$1 AND owner_id=$2 FOR SHARE`, provider, owner).Scan(&c.ProviderID, &c.ProviderName, &c.BaseURL, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return c, err
	}
	if !enabled {
		return c, failure(409, "PROVIDER_DISABLED")
	}
	c.Model = model
	err = tx.QueryRow(ctx, `INSERT INTO ai_conversations(owner_id,provider_id,model,assistant_id,assistant_revision,assistant_name,system_instruction) VALUES($1,$2,$3,NULLIF($4,'')::uuid,$5,$6,$7) RETURNING id,title,created_at`, owner, provider, model, c.AssistantID, c.AssistantRevision, c.AssistantName, c.SystemInstruction).Scan(&c.ID, &c.Title, &c.CreatedAt)
	if err != nil {
		return c, err
	}
	return c, tx.Commit(ctx)
}
func (s Store) Detail(ctx context.Context, owner, id string) (Conversation, []Generation, error) {
	var c Conversation
	if err := s.Reap(ctx, owner); err != nil {
		return c, nil, err
	}
	err := s.Pool.QueryRow(ctx, `SELECT c.id,c.provider_id,p.name,p.base_url,c.model,c.title,c.created_at,COALESCE(c.assistant_id::text,''),c.assistant_revision,c.assistant_name,c.system_instruction FROM ai_conversations c JOIN ai_providers p ON p.id=c.provider_id WHERE c.id=$1 AND c.owner_id=$2 AND c.deleted_at IS NULL`, id, owner).Scan(&c.ID, &c.ProviderID, &c.ProviderName, &c.BaseURL, &c.Model, &c.Title, &c.CreatedAt, &c.AssistantID, &c.AssistantRevision, &c.AssistantName, &c.SystemInstruction)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, nil, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return c, nil, err
	}
	rows, err := s.Pool.Query(ctx, `SELECT `+columns+` FROM ai_generations WHERE conversation_id=$1 AND owner_id=$2 ORDER BY ordinal`, id, owner)
	if err != nil {
		return c, nil, err
	}
	defer rows.Close()
	items := []Generation{}
	for rows.Next() {
		g, err := scan(rows)
		if err != nil {
			return c, nil, err
		}
		items = append(items, g)
	}
	return c, items, rows.Err()
}
