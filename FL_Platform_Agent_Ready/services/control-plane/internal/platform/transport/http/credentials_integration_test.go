//go:build integration

package transport

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
)

// Only positive-login fixtures call this helper. Do not upgrade aiDatabase or
// authorizationDB globally: their missing-schema compatibility cases must stay.
func configureCredentialLogin(t *testing.T, s *Server) []byte {
	t.Helper()
	if os.Getenv("FL_CREDENTIAL_TEST_ISOLATED") != "1" || os.Getenv("FL_PG_DATABASE") != "release_check" || os.Getenv("FL_PG_HOST") == "" || os.Getenv("FL_SECRETS_DIR") == "" || s.Identity.Pool == nil || s.Identity.Pool.Config().ConnConfig.Database == "flrp" {
		t.Fatal("requires isolated credential fixture")
	}
	raw, err := os.ReadFile("/src/db/migrations/000010_credential_limits.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	authorizationExec(t, s.Identity.Pool, string(raw))
	key := make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		t.Fatal(err)
	}
	s.Identity.Limits, err = credentials.New(s.Identity.Pool, key)
	if err != nil {
		t.Fatal(err)
	}
	s.Identity.Work = &credentials.WorkPool{}
	return key
}
func credentialFixture(t *testing.T) (*Server, []byte) {
	t.Helper()
	s := &Server{Identity: identity.Store{Pool: aiDatabase(t)}}
	key := configureCredentialLogin(t, s)
	for _, name := range []string{"credential-user", "independent"} {
		authorizationExec(t, s.Identity.Pool, "INSERT INTO users(external_subject,display_name,role,password_hash) VALUES($1,$1,'researcher',$2)", name, identity.PasswordHash("valid-password"))
	}
	return s, key
}
func credentialRequest(s *Server, raw, peer string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/v1/session", strings.NewReader(raw))
	r.RemoteAddr = peer
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func credentialBody(name, password string) string {
	raw, _ := json.Marshal(map[string]string{"username": name, "password": password})
	return string(raw)
}
func checkCredentialError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status%d, want%d", w.Code, status)
	}
	var result struct {
		Code, Message, RequestID string
		Retryable                bool
	}
	var body map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || json.Unmarshal(w.Body.Bytes(), &body) != nil || result.Code != code || result.Message != code || result.Retryable != (status == 429 || status >= 500) {
		t.Fatal("unsafe credential error")
	}
	if requestID, ok := body["request_id"].(string); !ok || requestID == "" {
		t.Fatal("missing correlation ID")
	}
	if status == 401 {
		if w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatal("missing credential challenge")
		}
	} else if w.Header().Get("WWW-Authenticate") != "" {
		t.Fatal("non-credential failure challenged session")
	}
	if status == 503 && w.Header().Get("Retry-After") != "3" {
		t.Fatal("missing dependency retry")
	}
}

func TestCredentialHTTPReplicasShareBudgetsAndIgnoreSpoofedHeaders(t *testing.T) {
	a, key := credentialFixture(t)
	pool, err := pgxpool.NewWithConfig(context.Background(), a.Identity.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	limits, err := credentials.New(pool, key)
	if err != nil {
		t.Fatal(err)
	}
	b := &Server{Identity: identity.Store{Pool: pool, Limits: limits, Work: &credentials.WorkPool{}}}
	first, second := httptest.NewServer(a), httptest.NewServer(b)
	defer first.Close()
	defer second.Close()
	request := func(client *http.Client, url, name, password string, status int) http.Header {
		t.Helper()
		r, err := http.NewRequest("POST", url+"/api/v1/session", strings.NewReader(credentialBody(name, password)))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-For", "198.51.100.77")
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != status {
			t.Fatalf("HTTP replica status%d, want%d", response.StatusCode, status)
		}
		var body map[string]any
		if err = json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if status == 200 {
			if token, ok := body["token"].(string); !ok || token == "" {
				t.Fatal("missing new session")
			}
		}
		return response.Header
	}
	for i := 0; i < 5; i++ {
		url := first.URL
		if i%2 == 1 {
			url = second.URL
		}
		request(first.Client(), url, "credential-user", "wrong", 401)
	}
	headers := request(second.Client(), second.URL, "credential-user", "wrong", 429)
	retry, err := strconv.Atoi(headers.Get("Retry-After"))
	if err != nil || retry < 1 || retry > 12 || headers.Get("WWW-Authenticate") != "" {
		t.Fatal("shared account retry not propagated")
	}
	// Saturate only the socket client's budget; XFF cannot select a fresh one.
	authorizationExec(t, pool, "UPDATE credential_limit_buckets SET tat=clock_timestamp()+interval '30 seconds',expires_at=clock_timestamp()+interval '1830 seconds' WHERE kind='client'")
	request(second.Client(), second.URL, "independent", "valid-password", 429)
	transport := &http.Transport{DialContext: (&net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP("127.0.0.2")}}).DialContext}
	defer transport.CloseIdleConnections()
	request(&http.Client{Transport: transport}, first.URL, "independent", "valid-password", 200)
}

func TestCredentialHTTPValidationAndGenericCredentialDenials(t *testing.T) {
	s, _ := credentialFixture(t)
	for _, raw := range []string{
		`{"username":"credential-user","password":"x","client":"203.0.113.1"}`,
		`{"username":"x","username":"y","password":"x"}`,
		`{"username":"x","password":"x"} {}`,
		credentialBody(strings.Repeat("x", 65), "x"), credentialBody("x", strings.Repeat("é", 65)), credentialBody("x", ""),
	} {
		checkCredentialError(t, credentialRequest(s, raw, "192.0.2.1:55"), 400, "INVALID_REQUEST")
	}
	if authorizationValue(t, s.Identity.Pool, "SELECT count(*)::text FROM credential_limit_buckets") != "0" {
		t.Fatal("invalid DTO consumed credential work state")
	}
	authorizationExec(t, s.Identity.Pool, "UPDATE users SET disabled_at=clock_timestamp() WHERE external_subject='independent'")
	for _, name := range []string{"credential-user", "unknown", "independent"} {
		checkCredentialError(t, credentialRequest(s, credentialBody(name, "wrong"), "192.0.2.1:55"), 401, "UNAUTHORIZED")
	}
	checkCredentialError(t, credentialRequest(s, credentialBody("x", "wrong"), "invalid-peer"), 503, "DEPENDENCY_UNAVAILABLE")
}

func TestCredentialHTTPMissingKeyAndSchemaPreserveExistingSession(t *testing.T) {
	s, key := credentialFixture(t)
	raw := credentialBody("credential-user", "valid-password")
	w := credentialRequest(s, raw, "192.0.2.1:55")
	if w.Code != 200 {
		t.Fatal("initial login failed")
	}
	var session struct{ Token string }
	if json.Unmarshal(w.Body.Bytes(), &session) != nil || session.Token == "" {
		t.Fatal("missing session")
	}
	me := func(method, path string, status int) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer "+session.Token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("existing-session status%d, want%d", w.Code, status)
		}
	}
	s.Identity.Limits = nil
	checkCredentialError(t, credentialRequest(s, raw, "192.0.2.1:55"), 503, "DEPENDENCY_UNAVAILABLE")
	me("GET", "/api/v1/me", 200)
	// Even if the key is available, an incomplete limiter schema fails before Verify.
	s.Identity.Limits, _ = credentials.New(s.Identity.Pool, key)
	authorizationExec(t, s.Identity.Pool, "ALTER TABLE credential_limit_buckets RENAME TO unavailable_limits")
	checkCredentialError(t, credentialRequest(s, raw, "192.0.2.1:55"), 503, "DEPENDENCY_UNAVAILABLE")
	me("GET", "/api/v1/me", 200)
	me("DELETE", "/api/v1/session", 200)
	me("GET", "/api/v1/me", 401)
}

func TestCredentialHTTPBusyFailsWithoutQueueOrAccountLookup(t *testing.T) {
	s, _ := credentialFixture(t)
	started, release, result := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		result <- s.Identity.Work.Do(context.Background(), func(context.Context) error { close(started); <-release; return nil })
	}()
	<-started
	checkCredentialError(t, credentialRequest(s, credentialBody("credential-user", "valid-password"), "192.0.2.1:55"), 503, "DEPENDENCY_UNAVAILABLE")
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if authorizationValue(t, s.Identity.Pool, "SELECT count(*)::text FROM credential_limit_buckets WHERE kind='account'") != "0" {
		t.Fatal("busy request reached account lookup")
	}
}
