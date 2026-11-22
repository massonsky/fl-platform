//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func aiDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "ai_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	for _, file := range []string{"000001_init.up.sql", "000002_runtime.up.sql", "000003_administration.up.sql", "000004_ai_providers.up.sql", "000005_ai_chat.up.sql", "000006_ai_assistants.up.sql"} {
		raw, err := os.ReadFile(filepath.Join("/src/db/migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	return pool
}

func TestAIProviderContract(t *testing.T) {
	pool := aiDatabase(t)
	ctx := context.Background()
	keys := inference.Keyring{Current: "v1", Keys: map[string]string{"v1": strings.Repeat("ab", 32)}}
	s := Server{Identity: identity.Store{Pool: pool}, Inference: inference.New(pool, keys, []string{"https://models.example/v1"})}
	users := map[string]string{}
	for _, role := range []string{"researcher", "admin", "viewer"} {
		id := uuid.NewString()
		users[role] = id
		if _, err := pool.Exec(ctx, `INSERT INTO users(id,external_subject,display_name,role) VALUES($1,$2,$3,$4)`, id, id, role, role); err != nil {
			t.Fatal(err)
		}
	}
	call := func(role, method, path, body string, status int) []byte {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.inference(w, r, identity.User{ID: users[role], Role: role}, strings.TrimPrefix(path, "/api/v1/"))
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-test-key") {
			t.Fatal("key leaked")
		}
		return w.Body.Bytes()
	}
	id := uuid.NewString()
	path := "/api/v1/ai/providers/" + id
	in := inference.Input{Name: "Personal", BaseURL: "https://models.example/v1", APIKey: "private-test-key", Enabled: true}
	encode := func() string { b, _ := json.Marshal(in); return string(b) }
	raw := call("researcher", "PUT", path, encode(), 201)
	var p inference.Provider
	if json.Unmarshal(raw, &p) != nil || p.Revision != 1 || !p.HasKey {
		t.Fatal("invalid provider response")
	}
	chatPath := "/api/v1/ai/conversations"
	call("viewer", "GET", chatPath, "", 403)
	chatInput := `{"provider_id":"` + id + `","model":"fixture","consent":true}`
	call("admin", "POST", chatPath, chatInput, 404)
	call("researcher", "POST", chatPath, strings.Replace(chatInput, "true", "false", 1), 400)
	var chat struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(call("researcher", "POST", chatPath, chatInput, 201), &chat) != nil || chat.ID == "" {
		t.Fatal("chat response")
	}
	call("researcher", "GET", chatPath+"/"+chat.ID, "", 200)
	call("admin", "GET", chatPath+"/"+chat.ID, "", 404)
	call("admin", "POST", chatPath+"/"+chat.ID+"/cancel", "", 404)
	call("admin", "DELETE", chatPath+"/"+chat.ID, "", 404)
	call("researcher", "POST", chatPath+"/"+chat.ID+"/messages", `{"request_id":"bad","content":"hello"}`, 400)
	call("researcher", "DELETE", chatPath+"/"+chat.ID, "", 200)
	call("researcher", "GET", chatPath+"/"+chat.ID, "", 404)
	call("researcher", "PUT", path, encode(), 409)
	call("viewer", "GET", "/api/v1/ai/providers", "", 403)
	raw = call("admin", "GET", "/api/v1/ai/providers", "", 200)
	var page struct {
		Items []inference.Provider `json:"items"`
	}
	if json.Unmarshal(raw, &page) != nil || len(page.Items) != 0 {
		t.Fatal("admin saw another user's providers")
	}
	in.Revision = 1
	in.APIKey = ""
	in.Enabled = false
	call("admin", "PUT", path, encode(), 404)
	call("admin", "POST", path+"/models", "", 404)
	call("researcher", "PUT", path, encode(), 200)
	call("researcher", "POST", path+"/models", "", 409)
	var encrypted []byte
	var version string
	if err := pool.QueryRow(ctx, `SELECT encrypted_secret,key_version FROM ai_providers WHERE id=$1`, id).Scan(&encrypted, &version); err != nil {
		t.Fatal(err)
	}
	plain, err := keys.Open(users["researcher"], id, version, encrypted)
	if err != nil || plain != "private-test-key" {
		t.Fatal("empty edit did not preserve key")
	}
	in.Revision = 2
	in.APIKey = "rotated-test-key"
	call("researcher", "PUT", path, encode(), 200)
	in.Revision = 3
	in.BaseURL = "https://evil.example"
	call("researcher", "PUT", path, encode(), 400)
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM administration_events WHERE target_id=$1`, id).Scan(&count); err != nil || count != 3 {
		t.Fatal("audit mismatch", count, err)
	}
	// Real top-level middleware must reject anonymous requests before reaching DB.
	r := httptest.NewRequest("GET", "/api/v1/ai/providers", nil)
	w := httptest.NewRecorder()
	s.serveHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("anonymous request accepted")
	}
}
