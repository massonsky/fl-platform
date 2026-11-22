package application

import (
	"context"
	"errors"
	aggregate "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/aggregation/domain"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/round/ports"
	"log/slog"
	"time"
)

type Repository interface {
	Active(context.Context) ([]string, error)
	Advance(context.Context, string) (*ports.Work, error)
	Inputs(context.Context, ports.Work) ([]ports.Input, error)
	Publish(context.Context, ports.Work, string, string, int) error
}
type Blobs interface {
	Get(context.Context, string, string, int64) ([]byte, error)
	Put(context.Context, []byte) (string, error)
}
type Coordinator struct {
	Repo  Repository
	Blobs Blobs
	Hash  func([]byte) string
}

func (c Coordinator) Aggregate(ctx context.Context, w ports.Work) error {
	inputs, err := c.Repo.Inputs(ctx, w)
	if err != nil {
		return err
	}
	accumulator, err := aggregate.New(int(inputs[0].Size))
	if err != nil {
		return err
	}
	for _, input := range inputs {
		if input.Schema != w.Schema {
			return errors.New("schema mismatch in frozen membership")
		}
		data, err := c.Blobs.Get(ctx, input.Key, input.Hash, input.Size)
		if err != nil {
			return err
		}
		if err = accumulator.Add(data, input.Count); err != nil {
			return err
		}
	}
	result, err := accumulator.Bytes()
	if err != nil {
		return err
	}
	key, err := c.Blobs.Put(ctx, result)
	if err != nil {
		return err
	}
	return c.Repo.Publish(ctx, w, key, c.Hash(result), len(result))
}
func (c Coordinator) Run(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := c.Repo.Active(ctx)
			if err != nil {
				slog.Error("coordinator state unavailable")
				continue
			}
			for _, id := range ids {
				work, err := c.Repo.Advance(ctx, id)
				if err == nil && work != nil {
					err = c.Aggregate(ctx, *work)
				}
				if err != nil {
					slog.Error("coordinator step failed", "run_id", id, "error", err.Error())
				}
			}
		}
	}
}
