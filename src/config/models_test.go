package config

import (
	"os"
	"path/filepath"
	"testing"
)

// isolate points the package at a temp dir so tests never touch the real
// .stubbs config.
func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	ProjectDir = dir
	ProjectConfigFile = filepath.Join(dir, "config.yaml")
	ProjectEnvFile = filepath.Join(dir, ".env")
}

func TestModelListLIFO(t *testing.T) {
	isolate(t)

	if got := Load().ActiveModel(); got != DefaultModel {
		t.Fatalf("default active = %q, want %q", got, DefaultModel)
	}

	for _, m := range []string{"a/one", "b/two", "a/one"} {
		if err := SaveModel(m); err != nil {
			t.Fatal(err)
		}
	}

	got := Load().Models
	want := []string{"a/one", "b/two"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("LIFO list = %v, want %v", got, want)
	}
}

func TestLegacyScalarModelMigrates(t *testing.T) {
	isolate(t)

	if err := os.WriteFile(ProjectConfigFile, []byte("provider: openrouter\nmodel: old/model\nenv: local\ntheme: tokyo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load().ActiveModel(); got != "old/model" {
		t.Fatalf("legacy active = %q, want old/model", got)
	}

	if err := SaveModel("new/model"); err != nil {
		t.Fatal(err)
	}
	got := Load().Models
	if len(got) != 2 || got[0] != "new/model" || got[1] != "old/model" {
		t.Fatalf("after migrating legacy model = %v", got)
	}
}
