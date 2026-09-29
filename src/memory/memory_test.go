package memory

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestNewStoreSeedsCoreFiles(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{projectFile, preferencesFile} {
		if _, err := os.Stat(filepath.Join(s.corePath(), name)); err != nil {
			t.Fatalf("core file %s not seeded: %v", name, err)
		}
	}
}

func TestNewStoreRequiresDir(t *testing.T) {
	if _, err := NewStore(Options{}); err == nil {
		t.Fatal("expected error for empty dir")
	}
}

func TestAddLoadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	created := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	in := Entry{
		Kind:       KindProcedure,
		Title:      "Run the tests",
		Body:       "Use `go test ./src/...` to run the suite.",
		Tags:       []string{"go", "test"},
		Importance: 0.75,
		Created:    created,
		Source:     "session_1",
	}
	if err := s.Add(in); err != nil {
		t.Fatalf("Add: %v", err)
	}

	entries, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	got := entries[0]
	if got.ID == "" {
		t.Error("ID should be generated")
	}
	if got.Kind != KindProcedure {
		t.Errorf("Kind = %q, want %q", got.Kind, KindProcedure)
	}
	if got.Title != in.Title || got.Body != in.Body {
		t.Errorf("title/body mismatch: %+v", got)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "go" {
		t.Errorf("tags = %v", got.Tags)
	}
	if got.Importance != 0.75 {
		t.Errorf("importance = %v", got.Importance)
	}
	if !got.Created.Equal(created) {
		t.Errorf("created = %v, want %v", got.Created, created)
	}
	if got.Source != "session_1" {
		t.Errorf("source = %q", got.Source)
	}
	if got.Updated.IsZero() {
		t.Error("updated should be set")
	}
}

func TestAddGeneratesUniqueIDs(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		if err := s.Add(Entry{Title: "Build commands", Body: "x"}); err != nil {
			t.Fatalf("Add: %v", err)
		}
	}
	entries, _ := s.Load()
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if seen[e.ID] {
			t.Fatalf("duplicate id %q", e.ID)
		}
		seen[e.ID] = true
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(Entry{Title: "Temp", Body: "x"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := s.Load()
	if err := s.Delete(entries[0].ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	after, _ := s.Load()
	if len(after) != 0 {
		t.Fatalf("got %d entries after delete, want 0", len(after))
	}
	if err := s.Delete(entries[0].ID); err == nil {
		t.Fatal("deleting a missing entry should error")
	}
}

func TestSearchRanksRelevantEntries(t *testing.T) {
	s := newTestStore(t)
	seed := []Entry{
		{Title: "Build and test commands", Body: "go build ./... and go test ./src/...", Tags: []string{"go", "build"}, Importance: 0.2},
		{Title: "Release checklist", Body: "Tag the commit, push the tag.", Tags: []string{"release"}},
		{Title: "Tabs not spaces", Body: "This project uses tabs for indentation.", Tags: []string{"style"}, Importance: 0.9},
	}
	for _, e := range seed {
		if err := s.Add(e); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.Search("how do I build and test", 2)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected matches")
	}
	if got[0].Title != "Build and test commands" {
		t.Fatalf("top result = %q, want build commands", got[0].Title)
	}

	none, err := s.Search("kubernetes", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected no matches, got %d", len(none))
	}
}

func TestSearchEmptyQueryOrdersByImportance(t *testing.T) {
	s := newTestStore(t)
	if err := s.Add(Entry{Title: "Low", Importance: 0.1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(Entry{Title: "High", Importance: 0.9}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Search("", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "High" {
		t.Fatalf("order = %+v, want High first", got)
	}
}

func TestCoreSkipsEmptyFiles(t *testing.T) {
	s := newTestStore(t)
	core, err := s.Core()
	if err != nil {
		t.Fatalf("Core: %v", err)
	}
	if len(core) != 0 {
		t.Fatalf("empty core files should be skipped, got %d", len(core))
	}

	if err := os.WriteFile(filepath.Join(s.corePath(), projectFile), []byte("Uses Go 1.25 and tabs."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.corePath(), preferencesFile), []byte("Never add comments."), 0o644); err != nil {
		t.Fatal(err)
	}
	core, err = s.Core()
	if err != nil {
		t.Fatal(err)
	}
	if len(core) != 2 {
		t.Fatalf("got %d core entries, want 2", len(core))
	}
	if core[0].Kind != KindProjectFact || core[1].Kind != KindPreference {
		t.Fatalf("kinds = %q, %q", core[0].Kind, core[1].Kind)
	}
}

func TestRepoMap(t *testing.T) {
	s := newTestStore(t)
	if got, err := s.RepoMap(); err != nil || got != "" {
		t.Fatalf("missing repo map = %q, %v", got, err)
	}
	if err := s.WriteRepoMap("src/agent holds the loop."); err != nil {
		t.Fatalf("WriteRepoMap: %v", err)
	}
	got, err := s.RepoMap()
	if err != nil {
		t.Fatal(err)
	}
	if got != "src/agent holds the loop." {
		t.Fatalf("repo map = %q", got)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Run  the Tests!": "run-the-tests",
		"":                "",
		"---":             "",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
