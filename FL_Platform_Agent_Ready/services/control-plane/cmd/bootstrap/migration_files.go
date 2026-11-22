package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type migration struct {
	name string
	sql  string
}

func loadMigrations(directory string) ([]migration, error) {
	entries, err := os.ReadDir(directory) // ReadDir returns filename order.
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	pattern := regexp.MustCompile(`^([0-9]{6})_[a-z][a-z0-9_]*\.up\.sql$`)
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, _ := strconv.Atoi(match[1])
		if version != len(migrations)+1 {
			return nil, fmt.Errorf("migration %q: expected sequence %06d (gap or duplicate)", entry.Name(), len(migrations)+1)
		}
		data, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(data)) == "" {
			return nil, fmt.Errorf("empty migration %s", entry.Name())
		}
		migrations = append(migrations, migration{strings.TrimSuffix(entry.Name(), ".up.sql"), string(data)})
	}
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no forward migrations in %s", directory)
	}
	return migrations, nil
}

// The ledger must be an exact prefix, including names: never baseline an unknown schema.
func pendingMigrations(available []migration, applied []string) ([]migration, error) {
	for i, name := range applied {
		if i >= len(available) || available[i].name != name {
			return nil, fmt.Errorf("migration ledger is not a prefix of this release at %q; reconcile schema before bootstrap", name)
		}
	}
	return available[len(applied):], nil
}
