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

func TestApprovalInWorkdirPreservedAcrossSaveModel(t *testing.T) {
	isolate(t)

	yaml := "provider: openrouter\nenv: local\ntheme: tokyo\napproval:\n  in_workdir: true\n  tools:\n    read: allow\n"
	if err := os.WriteFile(ProjectConfigFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Load().Approval.InWorkdir {
		t.Fatal("in_workdir not loaded")
	}

	if err := SaveModel("someone/model"); err != nil {
		t.Fatal(err)
	}
	if !Load().Approval.InWorkdir {
		t.Fatal("in_workdir lost after SaveModel")
	}
}

func TestInWorkdirMakesConfigCountAsSettings(t *testing.T) {
	isolate(t)

	if err := os.WriteFile(ProjectConfigFile, []byte("approval:\n  in_workdir: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !configFileHasSettings() {
		t.Fatal("in_workdir should count as a configured setting")
	}
}

func TestApprovalPreservedAcrossSaveModel(t *testing.T) {
	isolate(t)

	yaml := "provider: openrouter\nenv: local\ntheme: tokyo\napproval:\n  default: confirm\n  tools:\n    read: allow\n    list: allow\n"
	if err := os.WriteFile(ProjectConfigFile, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load().Approval.PolicyFor("read"); got != ApprovalAllow {
		t.Fatalf("read policy = %q, want %q", got, ApprovalAllow)
	}

	if err := SaveModel("someone/model"); err != nil {
		t.Fatal(err)
	}

	got := Load().Approval
	if got.PolicyFor("read") != ApprovalAllow || got.PolicyFor("list") != ApprovalAllow {
		t.Fatalf("approval lost after SaveModel: %+v", got)
	}
	if got.PolicyFor("bash") != ApprovalConfirm {
		t.Fatalf("unlisted bash policy = %q, want %q", got.PolicyFor("bash"), ApprovalConfirm)
	}
}
