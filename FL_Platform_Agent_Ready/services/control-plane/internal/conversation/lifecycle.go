package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
)

func (s Store) Active(ctx context.Context, owner, id string) (bool, error) {
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT status='streaming' AND deadline>now() FROM ai_generations WHERE owner_id=$1 AND request_id=$2`, owner, id).Scan(&active)
	return active, err
}
func (s Store) Checkpoint(ctx context.Context, owner, id, content string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE ai_generations SET content=$3 WHERE owner_id=$1 AND request_id=$2 AND status='streaming' AND deadline>now()`, owner, id, content)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return context.Canceled
	}
	return nil
}
func (s Store) Finish(ctx context.Context, owner, id, content, status, code, reason string, usage json.RawMessage) (Generation, error) {
	if len(usage) == 0 {
		usage = nil
	}
	_, err := s.Pool.Exec(ctx, `UPDATE ai_generations SET content=$3,status=CASE WHEN deadline<=now() THEN 'interrupted' ELSE $4 END,error_code=CASE WHEN deadline<=now() THEN 'GENERATION_TIMEOUT' ELSE $5 END,finish_reason=$6,usage=$7 WHERE owner_id=$1 AND request_id=$2 AND status='streaming'`, owner, id, content, status, code, reason, usage)
	if err != nil {
		return Generation{}, err
	}
	return scan(s.Pool.QueryRow(ctx, `SELECT `+columns+` FROM ai_generations WHERE owner_id=$1 AND request_id=$2`, owner, id))
}
func (s Store) Cancel(ctx context.Context, owner, conversation string, remove bool) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, owner).Scan(&id); err != nil {
		return err
	}
	err = tx.QueryRow(ctx, `SELECT id FROM ai_conversations WHERE id=$1 AND owner_id=$2 AND deleted_at IS NULL FOR UPDATE`, conversation, owner).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return failure(404, "NOT_FOUND")
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE ai_generations SET status='cancelled',error_code='GENERATION_CANCELLED' WHERE conversation_id=$1 AND owner_id=$2 AND status='streaming'`, conversation, owner); err != nil {
		return err
	}
	if remove {
		if _, err = tx.Exec(ctx, `UPDATE ai_generations SET input='',content='',usage=NULL WHERE conversation_id=$1 AND owner_id=$2`, conversation, owner); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE ai_conversations SET deleted_at=now(),title='Удалённый диалог',assistant_name='',system_instruction='' WHERE id=$1 AND owner_id=$2`, conversation, owner); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
