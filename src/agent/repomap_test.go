package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"stubbs/src/memory"
)

func TestBuildRepoDigest(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", "package main\nfunc main() {}\n")
	write("README.md", "# Project\n")
	write("sub/util.go", "package sub\n")
	write("node_modules/dep/index.js", "module.exports=1")
	write(".stubbs/secret.md", "should not appear")
	write("logo.png", "not text")

	got, err := buildRepoDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "main.go") || !strings.Contains(got, "sub/util.go") {
		t.Fatalf("tree missing files:\n%s", got)
	}
	if strings.Contains(got, "node_modules") {
		t.Fatalf("node_modules should be skipped:\n%s", got)
	}
	if strings.Contains(got, ".stubbs") {
		t.Fatalf(".stubbs should be skipped:\n%s", got)
	}
	if strings.Contains(got, "--- logo.png ---") {
		t.Fatalf("binary content should be skipped:\n%s", got)
	}
	if !strings.Contains(got, "# Project") || !strings.Contains(got, "func main") {
		t.Fatalf("key file contents missing:\n%s", got)
	}
}

func TestGenerateRepoMapWritesFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := memory.NewStore(memory.Options{Dir: t.TempDir(), AutoRepoMap: true})
	if err != nil {
		t.Fatal(err)
	}
	fc := &fakeCompleter{content: "# Repo map\nagent holds the loop"}
	a := &Agent{Memory: store, ModelClient: fc, WorkingDir: dir}

	out, err := a.GenerateRepoMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Repo map") {
		t.Fatalf("out = %q", out)
	}
	saved, err := store.RepoMap()
	if err != nil || !strings.Contains(saved, "agent holds the loop") {
		t.Fatalf("saved = %q, err = %v", saved, err)
	}
}

func TestEnsureRepoMapSkipsWhenPresent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := memory.NewStore(memory.Options{Dir: t.TempDir(), AutoRepoMap: true})
	if err := store.WriteRepoMap("existing map"); err != nil {
		t.Fatal(err)
	}
	fc := &fakeCompleter{content: "new"}
	a := &Agent{Memory: store, ModelClient: fc, WorkingDir: dir}
	a.ensureRepoMap(context.Background())
	if fc.called {
		t.Fatal("should not regenerate when a repo map already exists")
	}
}

func TestEnsureRepoMapDisabled(t *testing.T) {
	store, _ := memory.NewStore(memory.Options{Dir: t.TempDir()})
	fc := &fakeCompleter{content: "x"}
	a := &Agent{Memory: store, ModelClient: fc, WorkingDir: t.TempDir()}
	a.ensureRepoMap(context.Background())
	if fc.called {
		t.Fatal("should not generate when auto_repo_map is disabled")
	}
}

func TestEnsureRepoMapGeneratesWhenMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store, _ := memory.NewStore(memory.Options{Dir: t.TempDir(), AutoRepoMap: true})
	fc := &fakeCompleter{content: "# map"}
	a := &Agent{Memory: store, ModelClient: fc, WorkingDir: dir}
	a.ensureRepoMap(context.Background())
	if !fc.called {
		t.Fatal("expected repo map generation")
	}
	if saved, _ := store.RepoMap(); saved != "# map" {
		t.Fatalf("saved = %q", saved)
	}
}
