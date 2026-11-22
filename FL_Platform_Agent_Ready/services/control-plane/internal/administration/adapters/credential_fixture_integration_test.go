//go:build integration

package adapters

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
)

type credentialFixture struct {
	s     Service
	key   []byte
	users map[string]identity.Principal
}

func preparedFixture(t *testing.T) credentialFixture {
	t.Helper()
	if os.Getenv("FL_CREDENTIAL_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_DATABASE") != "release_check" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_SECRETS_DIR") == "" {
		t.Fatal("requires explicit isolated credential database/host/secrets")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	admin, err := db.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if credentialValue(t, admin, "SELECT current_database()") != "release_check" {
		admin.Close()
		t.Fatal("wrong fixture database")
	}
	name := "prepared_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	cfg := admin.Config()
	cfg.ConnConfig.Database = name
	pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Exact versions1..10 only; future migrations cannot silently alter this fixture.
	for version := 1; version <= 10; version++ {
		files, err := filepath.Glob(fmt.Sprintf("/migrations/%06d_*.up.sql", version))
		if err != nil || len(files) != 1 {
			t.Fatal("requires exactly one release migration per version1..10")
		}
		raw, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatal(err)
		}
		credentialExec(t, pool, string(raw))
	}
	f := credentialFixture{key: make([]byte, 32), users: map[string]identity.Principal{}}
	if _, err = rand.Read(f.key); err != nil {
		t.Fatal(err)
	}
	limits, err := credentials.New(pool, f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.s = Service{Pool: pool, Limits: limits, Work: &credentials.WorkPool{}}
	hash := identity.PasswordHash("fixture-old-password")
	for name, role := range map[string]string{"admin": "admin", "second": "admin", "owner": "researcher", "viewer": "viewer"} {
		p := identity.Principal{UserID: uuid.NewString(), SessionHash: identity.Hash(uuid.NewString())}
		credentialExec(t, pool, `INSERT INTO users(id,external_subject,display_name,role,password_hash) VALUES($1,$2,$2,$3,$4)`, p.UserID, name, role, hash)
		credentialExec(t, pool, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')`, p.SessionHash, p.UserID)
		f.users[name] = p
	}
	return f
}

func (f credentialFixture) replica(t *testing.T) Service {
	t.Helper()
	pool, err := pgxpool.NewWithConfig(context.Background(), f.s.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	limits, err := credentials.New(pool, f.key)
	if err != nil {
		t.Fatal(err)
	}
	return Service{Pool: pool, Limits: limits, Work: &credentials.WorkPool{}}
}

func (f credentialFixture) command(op string) CredentialMutation {
	no := false
	c := CredentialMutation{Client: netip.MustParseAddr("192.0.2.19"), Operation: op, Key: uuid.NewString()}
	switch op {
	case "user.create":
		c.Input = Input{Username: "new-" + uuid.NewString()[:8], Name: "Created", Role: "researcher", Password: "fixture-new-password"}
	case "user.update":
		c.Target = f.users["owner"].UserID
		c.Input = Input{Role: "researcher", Disabled: &no, Password: "fixture-new-password"}
	case "password.change":
		c.Input = Input{CurrentPassword: "fixture-old-password", Password: "fixture-new-password"}
	}
	raw, _ := json.Marshal(c.Input)
	h := hmac.New(sha256.New, f.key)
	h.Write([]byte(c.Operation + "/" + c.Target + "\n"))
	h.Write(raw)
	c.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return c
}

func credentialExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}
func credentialValue(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var value string
	if err := pool.QueryRow(ctx, sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func credentialState(t *testing.T, pool *pgxpool.Pool) string {
	return credentialValue(t, pool, `SELECT jsonb_build_object('users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY token_hash) FROM sessions s),
 'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM administration_events e),
 'receipts',(SELECT jsonb_agg(to_jsonb(r) ORDER BY actor_id,operation,idempotency_key) FROM administration_receipts r))::text`)
}
func credentialFailure(t *testing.T, output json.RawMessage, err error, status int, code string) {
	t.Helper()
	var a Failure
	var c credentials.Failure
	var o organization.Failure
	gotStatus, gotCode := 0, ""
	if errors.As(err, &a) {
		gotStatus, gotCode = a.Status, a.Code
	} else if errors.As(err, &c) {
		gotStatus, gotCode = c.Status, c.Code
	} else if errors.As(err, &o) {
		gotStatus, gotCode = o.Status, o.Code
	}
	if output != nil || gotStatus != status || gotCode != code {
		t.Fatalf("unexpected result: status%d code%s, want%d %s", gotStatus, gotCode, status, code)
	}
}
func credentialWrite(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := db.Transaction(ctx, pool, func(tx pgx.Tx) error {
		if err := authorization.Write(ctx, tx); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
	if err != nil {
		t.Fatal("independent guarded write failed", err)
	}
}

type credentialResult struct {
	output json.RawMessage
	err    error
}

func pauseCredential(t *testing.T, s Service, p identity.Principal, c CredentialMutation, ctx context.Context, phase string) (func(), <-chan credentialResult) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan credentialResult, 1)
	resume := func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}
	t.Cleanup(resume)
	if phase == "verify" {
		s.verifyPassword = func(string, string) bool { close(started); <-release; return true }
	} else {
		s.passwordHash = func(string) string { close(started); <-release; return "controlled-prepared-hash" }
	}
	go func() { out, err := s.MutateCredential(ctx, p, c); done <- credentialResult{out, err} }()
	select {
	case <-started:
	case result := <-done:
		t.Fatalf("work ended before pause: %v", result.err)
	case <-time.After(3 * time.Second):
		t.Fatal("work did not reach pause")
	}
	return resume, done
}
func awaitCredential(t *testing.T, done <-chan credentialResult) credentialResult {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("credential operation did not complete")
		return credentialResult{}
	}
}
