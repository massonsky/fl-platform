package main

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	pb "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/gen/go/flplatform/v1"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	authlimits "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/credentials"
	experiment "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/experiment/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/inference"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/organization"
	participant "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/participant/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/config"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	grpcapi "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/transport/grpc"
	httpapi "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/transport/http"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/projectaccess"
	round "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/adapters"
	scheduler "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/scheduler/adapters"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	pool, err := db.Open(ctx)
	must(err)
	defer pool.Close()
	blobs, err := artifact.Open()
	must(err)
	registry, err := participant.New(pool)
	must(err)
	key, err := config.Secret("session_key")
	must(err)
	// Missing/malformed limiter key closes new Login, not existing authenticated
	// routes. Never log the key/read error or reuse the session/cursor key.
	var limits *authlimits.Store
	if encoded, e := config.Secret("auth_limit_key"); e == nil && len(encoded) == 64 {
		if raw, e := hex.DecodeString(encoded); e == nil {
			limits, _ = authlimits.New(pool, raw)
		}
	}
	work := &authlimits.WorkPool{}
	if limits == nil {
		slog.Warn("credential login unavailable: limiter key not configured")
	}
	if limits != nil {
		go func() {
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := limits.Collect(ctx); err != nil {
						slog.Warn("credential limit maintenance unavailable")
					}
				}
			}
		}()
	}
	var ai *inference.Store
	if os.Getenv("FL_AI_ENABLED") == "true" {
		raw, e := config.Secret("ai_keyring")
		must(e)
		keys, e := inference.ParseKeyring(raw)
		must(e)
		ai = inference.New(pool, keys, strings.Split(os.Getenv("FL_AI_ALLOWED_BASE_URLS"), ","))
	}
	var organizations *organization.Service
	if os.Getenv("FL_ORGANIZATIONS_ENABLED") == "true" {
		organizations = &organization.Service{Pool: pool}
	}
	var projects *projectaccess.Service
	if os.Getenv("FL_ORGANIZATION_PROJECTS_ENABLED") == "true" {
		if organizations == nil {
			must(errors.New("organization projects require FL_ORGANIZATIONS_ENABLED"))
		}
		projects = &projectaccess.Service{Pool: pool}
	}
	httpServer := &http.Server{Addr: ":8080", Handler: &httpapi.Server{Inference: ai, Organizations: organizations, ProjectAccess: projects, Identity: identity.Store{Pool: pool, Limits: limits, Work: work}, Experiments: experiment.Store{Pool: pool, Blobs: blobs}, CursorKey: []byte(key)}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	cert, err := tls.LoadX509KeyPair("/run/secrets/server.crt", "/run/secrets/server.key")
	must(err)
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})), grpc.MaxRecvMsgSize(2<<20), grpc.MaxConcurrentStreams(32))
	service := &grpcapi.Server{Pool: pool, Registry: registry, Blobs: blobs, Scheduler: scheduler.Store{Pool: pool}, Rounds: round.Store{Pool: pool}}
	pb.RegisterParticipantServiceServer(grpcServer, service)
	pb.RegisterArtifactServiceServer(grpcServer, service)
	listener, err := net.Listen("tcp", ":50051")
	must(err)
	go func() {
		if err := grpcServer.Serve(listener); err != nil {
			slog.Error("grpc stopped")
			stop()
		}
	}()
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http stopped")
			stop()
		}
	}()
	slog.Info("API ready")
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
	grpcServer.Stop()
}
func must(err error) {
	if err != nil {
		slog.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}
