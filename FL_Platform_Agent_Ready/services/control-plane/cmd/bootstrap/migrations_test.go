package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMigrationsDiscoversLaterReleaseFilesInOrder(t *testing.T) {
	directory := t.TempDir()
	for _, name := range []string{"000003_future.up.sql", "000001_init.up.sql", "000002_runtime.up.sql", "000002_runtime.down.sql", "README.md"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("SELECT 1;"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	migrations, err := loadMigrations(directory)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, migration := range migrations {
		names = append(names, migration.name)
		if migration.sql != "SELECT 1;" {
			t.Fatalf("SQL not loaded for %s", migration.name)
		}
	}
	if want := []string{"000001_init", "000002_runtime", "000003_future"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("migrations %v, want %v", names, want)
	}
}

func TestLoadMigrationsRejectsIncompleteOrAmbiguousRelease(t *testing.T) {
	for _, test := range []struct {
		name  string
		files map[string]string
	}{
		{"empty", nil},
		{"gap", map[string]string{"000001_init.up.sql": "SELECT 1;", "000003_later.up.sql": "SELECT 3;"}},
		{"duplicate", map[string]string{"000001_init.up.sql": "SELECT 1;", "000001_other.up.sql": "SELECT 2;"}},
		{"invalid name", map[string]string{"1_init.up.sql": "SELECT 1;"}},
		{"empty SQL", map[string]string{"000001_init.up.sql": " \n"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			for name, data := range test.files {
				if err := os.WriteFile(filepath.Join(directory, name), []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := loadMigrations(directory); err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestMigrationPlanFreshUpgradeAndRepeat(t *testing.T) {
	available := []migration{{name: "000001_init"}, {name: "000002_runtime"}, {name: "000003_ai"}}
	for _, applied := range [][]string{nil, {"000001_init"}, {"000001_init", "000002_runtime", "000003_ai"}} {
		pending, err := pendingMigrations(available, applied)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(pending, available[len(applied):]) {
			t.Fatalf("incorrect pending migrations after %v: %v", applied, pending)
		}
	}
}

func TestMigrationPlanRejectsUnknownRenamedAndGappedLedger(t *testing.T) {
	available := []migration{{name: "000001_init"}, {name: "000002_runtime"}}
	for _, applied := range [][]string{
		{"000001_init", "000002_runtime", "000003_newer_release"},
		{"000001_renamed"},
		{"000002_runtime"},
		{"000001_init", "000001_init"},
	} {
		if _, err := pendingMigrations(available, applied); err == nil {
			t.Fatalf("unsafe ledger accepted: %v", applied)
		}
	}
}
