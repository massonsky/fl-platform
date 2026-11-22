//go:build integration

package transport

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

func TestDependencyFailureAndSessionRecovery(t *testing.T) {
	base := aiDatabase(t)
	ctx := context.Background()
	var disconnected atomic.Bool
	cfg := base.Config()
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		if disconnected.Load() {
			return nil, errors.New("test database connection unavailable")
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `INSERT INTO users(external_subject,display_name,role,password_hash) VALUES('release-user','Release','researcher',$1)`, identity.PasswordHash("test-password"))
	if err != nil {
		t.Fatal(err)
	}
	var s3Status atomic.Int32
	s3Status.Store(200)
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Query().Has("location") {
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`))
			return
		}
		w.WriteHeader(int(s3Status.Load()))
	}))
	defer s3.Close()
	secrets := t.TempDir()
	for _, name := range []string{"minio_user", "minio_password"} {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte("test-value"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("FL_SECRETS_DIR", secrets)
	t.Setenv("FL_S3_ENDPOINT", strings.TrimPrefix(s3.URL, "http://"))
	t.Setenv("FL_S3_BUCKET", "release-check")
	blobs, err := artifact.Open()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Identity: identity.Store{Pool: pool}, Experiments: experiment.Store{Pool: pool, Blobs: blobs}}
	configureCredentialLogin(t, s)
	call := func(method, path, token, body string, status int) map[string]any {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if status == 503 && w.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("outage challenged valid credentials")
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if status == 503 && path != "/readyz" {
			if result["code"] != "DEPENDENCY_UNAVAILABLE" || result["retryable"] != true {
				t.Fatal(result)
			}
		}
		return result
	}
	login := `{"username":"release-user","password":"test-password"}`
	token := call("POST", "/api/v1/session", "", login, 200)["token"].(string)
	call("GET", "/api/v1/me", token, "", 200)
	call("GET", "/api/v1/me", "invalid", "", 401)
	call("POST", "/api/v1/session", "", `{"username":"release-user","password":"wrong"}`, 401)
	call("POST", "/api/v1/session", "", `{"username":"unknown","password":"wrong"}`, 401)
	call("GET", "/readyz", "", "", 200)
	s3Status.Store(403)
	call("GET", "/readyz", "", "", 503)
	call("GET", "/healthz", "", "", 200)
	s3Status.Store(200)
	call("GET", "/readyz", "", "", 200)
	disconnected.Store(true)
	pool.Reset()
	call("GET", "/api/v1/me", token, "", 503)
	call("POST", "/api/v1/session", "", login, 503)
	call("GET", "/readyz", "", "", 503)
	call("GET", "/healthz", "", "", 200)
	disconnected.Store(false)
	call("GET", "/readyz", "", "", 200)
	call("GET", "/api/v1/me", token, "", 200)
	call("DELETE", "/api/v1/session", token, "", 200)
	call("GET", "/api/v1/me", token, "", 401)
}
