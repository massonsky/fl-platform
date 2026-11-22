//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/assistant"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/conversation"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAssistantContract(t *testing.T) {
	pool := aiDatabase(t)
	ctx := context.Background()
	owner, other, provider, id := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, u := range []string{owner, other} {
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,'fixture','researcher')`, u, u); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ai_providers(id,owner_id,name,base_url,encrypted_secret,key_version) VALUES($1,$2,'fixture','https://models.example/v1','cipher','v1')`, provider, owner); err != nil {
		t.Fatal(err)
	}
	s := Server{Identity: identity.Store{Pool: pool}, Inference: inference.New(pool, inference.Keyring{Current: "v1", Keys: map[string]string{"v1": strings.Repeat("ab", 32)}}, []string{"https://models.example/v1"})}
	call := func(user, role, method, path, body string, status int) []byte {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "/api/v1/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		s.inference(w, r, identity.User{ID: user, Role: role}, path)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		return w.Body.Bytes()
	}
	in := assistant.Input{Name: "Помощник", ProviderID: provider, Model: "model", Instruction: "Отвечай кратко.", Enabled: true}
	encode := func() string { b, _ := json.Marshal(in); return string(b) }
	path := "ai/assistants/" + id
	call(owner, "viewer", "GET", "ai/assistants", "", 403)
	call(other, "admin", "PUT", path, encode(), 404)
	call(owner, "researcher", "PUT", path, encode(), 201)
	call(owner, "researcher", "PUT", path, encode(), 409)
	if string(call(other, "admin", "GET", "ai/assistants", "", 200)) != "{\"items\":[]}\n" {
		t.Fatal("foreign profile visible")
	}
	chats := conversation.Store{Pool: pool}
	if _, err := chats.CreateWithAssistant(ctx, owner, provider, "model", true, id, 2); err == nil {
		t.Fatal("stale revision accepted")
	}
	if _, err := chats.CreateWithAssistant(ctx, owner, provider, "other-model", true, id, 1); err == nil {
		t.Fatal("model mismatch")
	}
	c, err := chats.CreateWithAssistant(ctx, owner, provider, "model", true, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	in.Revision = 1
	in.Instruction = "Новая инструкция"
	in.Enabled = false
	call(owner, "researcher", "PUT", path, encode(), 200)
	if _, err = chats.CreateWithAssistant(ctx, owner, provider, "model", true, id, 2); err == nil {
		t.Fatal("disabled profile accepted")
	}
	detail, _, err := chats.Detail(ctx, owner, c.ID)
	if err != nil || detail.SystemInstruction != "Отвечай кратко." || detail.AssistantRevision == nil || *detail.AssistantRevision != 1 {
		t.Fatal("snapshot changed", err)
	}
	job, err := chats.Begin(ctx, owner, c.ID, uuid.NewString(), "Вопрос", "fingerprint")
	if err != nil || len(job.Messages) != 2 || job.Messages[0].Role != "system" || job.Messages[0].Content != "Отвечай кратко." {
		t.Fatal("missing system snapshot", err)
	}
	if err = chats.Cancel(ctx, owner, c.ID, true); err != nil {
		t.Fatal(err)
	}
	var instruction string
	if err = pool.QueryRow(ctx, `SELECT system_instruction FROM ai_conversations WHERE id=$1`, c.ID).Scan(&instruction); err != nil || instruction != "" {
		t.Fatal("snapshot retained after delete")
	}
	in.Revision = 2
	in.Instruction = strings.Repeat("я", 9000)
	call(owner, "researcher", "PUT", path, encode(), 400)
	in.Instruction = strings.Repeat("x", 16384)
	in.Enabled = true
	call(owner, "researcher", "PUT", path, encode(), 200)
	c, err = chats.CreateWithAssistant(ctx, owner, provider, "model", true, id, 3)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		_, err = pool.Exec(ctx, `INSERT INTO ai_generations(owner_id,request_id,conversation_id,fingerprint,input,content,status,provider_revision) VALUES($1,$2,$3,'fixture','input',$4,'completed',1)`, owner, uuid.NewString(), c.ID, strings.Repeat("a", 30000))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = chats.Begin(ctx, owner, c.ID, uuid.NewString(), "next", "budget"); err == nil {
		t.Fatal("system instruction excluded from context limit")
	}
}
