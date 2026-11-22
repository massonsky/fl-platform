package conversation

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
)

type Job struct {
	Generation        Generation
	ProviderID, Model string
	Revision          int
	Messages          []inference.ChatMessage
	Replay            bool
}

func (s Store) Begin(ctx context.Context, owner, conversation, id, content, fingerprint string) (Job, error) {
	var job Job
	if _, err := uuid.Parse(id); err != nil || strings.TrimSpace(content) == "" || len(content) > 32<<10 || !utf8.ValidString(content) || strings.ContainsRune(content, 0) {
		return job, failure(400, "INVALID_REQUEST")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return job, err
	}
	defer tx.Rollback(ctx)
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&locked); err != nil {
		return job, err
	}
	_, err = tx.Exec(ctx, `UPDATE ai_generations SET status='interrupted',error_code='GENERATION_INTERRUPTED' WHERE owner_id=$1 AND status='streaming' AND deadline<=now()`, owner)
	if err != nil {
		return job, err
	}
	var enabled bool
	var instruction string
	err = tx.QueryRow(ctx, `SELECT c.provider_id,c.model,p.revision,p.enabled,c.system_instruction FROM ai_conversations c JOIN ai_providers p ON p.id=c.provider_id AND p.owner_id=c.owner_id WHERE c.id=$1 AND c.owner_id=$2 AND c.deleted_at IS NULL FOR SHARE OF c,p`, conversation, owner).Scan(&job.ProviderID, &job.Model, &job.Revision, &enabled, &instruction)
	if errors.Is(err, pgx.ErrNoRows) {
		return job, failure(404, "NOT_FOUND")
	}
	if err != nil {
		return job, err
	}
	var old string
	err = tx.QueryRow(ctx, `SELECT fingerprint FROM ai_generations WHERE owner_id=$1 AND request_id=$2`, owner, id).Scan(&old)
	if err == nil {
		if old != fingerprint {
			return job, failure(409, "IDEMPOTENCY_CONFLICT")
		}
		job.Generation, err = scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM ai_generations WHERE owner_id=$1 AND request_id=$2`, owner, id))
		if err != nil {
			return job, err
		}
		if job.Generation.ConversationID != conversation {
			return job, failure(409, "IDEMPOTENCY_CONFLICT")
		}
		job.Replay = true
		return job, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return job, err
	}
	if !enabled {
		return job, failure(409, "PROVIDER_DISABLED")
	}
	var active, recent int
	err = tx.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status='streaming'),count(*) FILTER(WHERE created_at>now()-interval '1 minute') FROM ai_generations WHERE owner_id=$1`, owner).Scan(&active, &recent)
	if err != nil {
		return job, err
	}
	if active > 0 {
		return job, failure(409, "GENERATION_ACTIVE")
	}
	if recent >= 10 {
		return job, failure(429, "RATE_LIMITED")
	}
	var turns, size int64
	err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(octet_length(input)+octet_length(content)),0) FROM ai_generations WHERE conversation_id=$1 AND owner_id=$2`, conversation, owner).Scan(&turns, &size)
	if err != nil {
		return job, err
	}
	if turns >= 128 || size+int64(len(content)+len(instruction)) > 128<<10 {
		return job, failure(409, "HISTORY_TOO_LARGE")
	}
	if instruction != "" {
		job.Messages = append(job.Messages, inference.ChatMessage{Role: "system", Content: instruction})
	}
	rows, err := tx.Query(ctx, `SELECT input,content FROM ai_generations WHERE conversation_id=$1 AND owner_id=$2 AND status='completed' ORDER BY ordinal`, conversation, owner)
	if err != nil {
		return job, err
	}
	for rows.Next() {
		var input, output string
		if err = rows.Scan(&input, &output); err != nil {
			rows.Close()
			return job, err
		}
		job.Messages = append(job.Messages, inference.ChatMessage{Role: "user", Content: input}, inference.ChatMessage{Role: "assistant", Content: output})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return job, err
	}
	job.Messages = append(job.Messages, inference.ChatMessage{Role: "user", Content: content})
	job.Generation, err = scan(tx.QueryRow(ctx, `INSERT INTO ai_generations(owner_id,request_id,conversation_id,fingerprint,input,status,provider_revision) VALUES($1,$2,$3,$4,$5,'streaming',$6) RETURNING `+columns, owner, id, conversation, fingerprint, content, job.Revision))
	if err != nil {
		return job, err
	}
	if turns == 0 {
		title := []rune(strings.TrimSpace(content))
		if len(title) > 60 {
			title = title[:60]
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_conversations SET title=$3 WHERE id=$1 AND owner_id=$2`, conversation, owner, string(title)); err != nil {
			return job, err
		}
	}
	return job, tx.Commit(ctx)
}
