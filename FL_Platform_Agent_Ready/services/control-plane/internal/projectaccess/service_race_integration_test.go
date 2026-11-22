//go:build integration

package projectaccess

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/authorization"
)

func waitServiceGuard(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var waiting bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND NOT granted
 AND database=(SELECT oid FROM pg_database WHERE datname=current_database()))`).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("operation did not wait on authorization guard")
}

func TestServiceExpiryAfterGuardWaitRejectsReadAndWrite(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	s := Service{Pool: pool}
	for _, action := range []string{"read", "create"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p := servicePrincipal(t, pool, owner)
			accessExec(t, pool, "UPDATE sessions SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE token_hash=$1", p.SessionHash)
			blocker, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if err = authorization.Write(ctx, blocker); err != nil {
				t.Fatal(err)
			}
			before := serviceSnapshot(t, pool)
			done := make(chan error, 1)
			go func() {
				if action == "read" {
					_, err := s.Get(ctx, p, org, project)
					done <- err
					return
				}
				_, err := s.Create(ctx, p, org, uuid.NewString(), CreateInput{"expired-create", "Expired"})
				done <- err
			}()
			waitServiceGuard(t, pool)
			for {
				var expired bool
				if err := pool.QueryRow(ctx, "SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1", p.SessionHash).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			accessFailure(t, <-done, 401, "UNAUTHORIZED")
			if serviceSnapshot(t, pool) != before {
				t.Fatal("expired guard waiter wrote data")
			}
		})
	}
}

func TestServiceLogoutWaitsForProtectedWorkAndRevokesNextRequest(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	s := Service{Pool: pool}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	token := uuid.NewString()
	p := identity.Principal{UserID: owner, SessionHash: identity.Hash(token)}
	accessExec(t, pool, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,clock_timestamp()+interval '1 hour')", p.SessionHash, owner)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	read, logout := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := serviceCall(ctx, s, p, false, func(ctx context.Context, tx pgx.Tx, user identity.User) (Project, error) {
			project, err := Check(ctx, tx, user.ID, ResourceRef{"project", project, org}, Read)
			if err != nil {
				return Project{}, err
			}
			close(entered)
			select {
			case <-release:
				return project, nil
			case <-ctx.Done():
				return Project{}, ctx.Err()
			}
		})
		read <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	go func() { logout <- (identity.Store{Pool: pool}).Logout(ctx, token) }()
	waitServiceGuard(t, pool)
	select {
	case err := <-logout:
		t.Fatal("logout crossed protected transaction", err)
	default:
	}
	once.Do(func() { close(release) })
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if err := <-logout; err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, p, org, project)
	accessFailure(t, err, 401, "UNAUTHORIZED")
	if got.ID != "" {
		t.Fatal("revoked session returned project")
	}
}

func TestServiceCallerDeadlineBoundsGuardWait(t *testing.T) {
	pool, _, owner, org, project := accessFixture(t)
	p := servicePrincipal(t, pool, owner)
	blocker, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if err = authorization.Write(context.Background(), blocker); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	got, err := (Service{Pool: pool}).Get(ctx, p, org, project)
	accessFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if got.ID != "" {
		t.Fatal("timed-out transaction returned DTO")
	}
}

func TestServiceOperationDeadlineReachesHelperSQL(t *testing.T) {
	pool, _, owner, org, _ := accessFixture(t)
	p := servicePrincipal(t, pool, owner)
	accessExec(t, pool, `CREATE FUNCTION slow_project_insert() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN PERFORM pg_sleep(15); RETURN NEW; END$$;
 CREATE TRIGGER slow_project_insert BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION slow_project_insert()`)
	before := serviceSnapshot(t, pool)
	started := time.Now()
	got, err := (Service{Pool: pool}).Create(context.Background(), p, org, uuid.NewString(), CreateInput{"deadline", "Deadline"})
	accessFailure(t, err, 503, "DEPENDENCY_UNAVAILABLE")
	if time.Since(started) > authorization.OperationTimeout+2*time.Second {
		t.Fatal("helper SQL captured an unbounded caller context")
	}
	if got.ID != "" || serviceSnapshot(t, pool) != before {
		t.Fatal("deadline failure returned DTO or persisted writes")
	}
}
