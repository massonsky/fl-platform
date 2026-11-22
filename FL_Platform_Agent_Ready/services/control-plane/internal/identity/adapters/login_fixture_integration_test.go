//go:build integration

package adapters

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

func loginFixture(t *testing.T) (Store, string) {
	t.Helper()
	if os.Getenv("FL_CREDENTIAL_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_DATABASE") != "release_check" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires explicit isolated credential database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	name := "login_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
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
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	files, err := filepath.Glob("/migrations/*.up.sql")
	if err != nil || len(files) < 10 {
		t.Fatal("requires migrations1-10", err)
	}
	for _, file := range files[:10] {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		loginExec(t, pool, string(raw))
	}
	limits, err := credentials.New(pool, bytes.Repeat([]byte{61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	loginExec(t, pool, "INSERT INTO users(id,external_subject,display_name,role,password_hash) VALUES($1,'login-user','Test','researcher',$2)", id, dummyPasswordHash)
	return Store{Pool: pool, Limits: limits, Work: &credentials.WorkPool{}}, id
}
func loginExec(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func inputForLogin() LoginInput {
	return LoginInput{Client: netip.MustParseAddr("192.0.2.3"), Username: "login-user", Password: "test-password"}
}
func loginSessions(t *testing.T, s Store) int {
	t.Helper()
	var n int
	if err := s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func assertLoginFailure(t *testing.T, token string, err error, status int) {
	t.Helper()
	if token != "" {
		t.Fatal("failure returned a token")
	}
	if status == 401 {
		if !errors.Is(err, ErrUnauthorized) {
			t.Fatalf("expected unauthorized, got %v", err)
		}
		return
	}
	var f credentials.Failure
	if !errors.As(err, &f) || f.Status != status || f.Code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("expected safe%d, got %v", status, err)
	}
}

type loginResult struct {
	token string
	err   error
}

func pausedLogin(t *testing.T, s *Store, ctx context.Context) (chan struct{}, <-chan loginResult) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	s.verifyPassword = func(string, string) bool { close(started); <-release; return true }
	result := make(chan loginResult, 1)
	go func() { token, err := s.Login(ctx, inputForLogin()); result <- loginResult{token, err} }()
	select {
	case <-started:
	case early := <-result:
		t.Fatalf("login failed before controlled work: %v", early.err)
	case <-time.After(3 * time.Second):
		t.Fatal("login never reached controlled work")
	}
	return release, result
}
