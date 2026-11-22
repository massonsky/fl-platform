package main

import (
	"context"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/application"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
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
	coordinator := application.Coordinator{Repo: adapters.Store{Pool: pool}, Blobs: blobs, Hash: artifact.Hash}
	slog.Info("orchestrator started")
	coordinator.Run(ctx)
}
func must(err error) {
	if err != nil {
		slog.Error("startup failed", "error", err.Error())
		os.Exit(1)
	}
}
