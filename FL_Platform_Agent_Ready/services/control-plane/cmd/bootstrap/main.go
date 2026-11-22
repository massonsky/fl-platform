package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	artifact "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/artifact/adapters"
	identity "github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/identity/adapters"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/config"
	"github.com/massonsky/fl-platform/FL_Platform_Agent_Ready/services/control-plane/internal/platform/db"
	"os"
	"path/filepath"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "bootstrap failed:", err)
		os.Exit(1)
	}
}
func run() error {
	ctx := context.Background()
	pool, err := db.Open(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = migrate(ctx, pool); err != nil {
		return err
	}
	password, err := config.Secret("admin_password")
	if err != nil {
		return err
	}
	if err = bootstrapAdmin(ctx, pool, identity.PasswordHash(password)); err != nil {
		return err
	}
	blobs, err := artifact.Open()
	if err != nil {
		return err
	}
	data, err := os.ReadFile("/presets/presets.json")
	if err != nil {
		return err
	}
	var presets []struct {
		ID        string          `json:"id"`
		Config    json.RawMessage `json:"config"`
		Schema    json.RawMessage `json:"schema"`
		Dataset   json.RawMessage `json:"dataset_manifest"`
		Partition json.RawMessage `json:"partition_manifest"`
		Runtime   json.RawMessage `json:"runtime_manifest"`
		Weights   string          `json:"weights_file"`
	}
	if err = json.Unmarshal(data, &presets); err != nil {
		return err
	}
	for _, p := range presets {
		var exists bool
		if err = pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM model_presets WHERE id=$1)", p.ID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if filepath.Base(p.Weights) != p.Weights {
			return fmt.Errorf("invalid preset filename")
		}
		weights, err := os.ReadFile("/presets/" + p.Weights)
		if err != nil {
			return err
		}
		var c struct {
			Schema  string `json:"model_schema_hash"`
			Initial string `json:"initial_weights_hash"`
		}
		if err = json.Unmarshal(p.Config, &c); err != nil {
			return err
		}
		if artifact.Hash(weights) != c.Initial {
			return fmt.Errorf("initial weights hash mismatch")
		}
		key, err := blobs.Put(ctx, weights)
		if err != nil {
			return err
		}
		err = db.Transaction(ctx, pool, func(tx pgx.Tx) error {
			aid, err := artifact.Record(ctx, tx, key, c.Initial, c.Schema, len(weights), nil, nil)
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `INSERT INTO model_presets(id,config_json,schema_json,dataset_manifest,partition_manifest,runtime_manifest,initial_artifact_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, p.ID, p.Config, p.Schema, p.Dataset, p.Partition, p.Runtime, aid)
			return err
		})
		if err != nil {
			return err
		}
	}
	fmt.Println("Bootstrap complete")
	return nil
}
