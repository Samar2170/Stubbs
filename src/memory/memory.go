package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"gopkg.in/yaml.v3"
)

type Kind string

const (
	KindProjectFact Kind = "project-fact"
	KindPreference  Kind = "preference"
	KindProcedure   Kind = "procedure"
	KindEpisode     Kind = "episode"
	KindRepoMap     Kind = "repo-map"
)

const (
	coreDir     = "core"
	entriesDir  = "entries"
	repoMapFile = "repo-map.md"

	projectFile     = "project.md"
	preferencesFile = "preferences.md"
)

type Entry struct {
	ID         string
	Kind       Kind
	Title      string
	Body       string
	Tags       []string
	Importance float32
	Created    time.Time
	Updated    time.Time
	Source     string
}

type Options struct {
	Dir               string
	BudgetTokens      int
	TopK              int
	AutoSummarize     bool
	AutoRepoMap       bool
	CaptureHeuristics bool
}

type Store struct {
	dir  string
	opts Options
}

type frontmatter struct {
	ID         string    `yaml:"id"`
	Kind       string    `yaml:"kind,omitempty"`
	Title      string    `yaml:"title,omitempty"`
	Tags       []string  `yaml:"tags,omitempty"`
	Importance float32   `yaml:"importance,omitempty"`
	Created    time.Time `yaml:"created,omitempty"`
	Updated    time.Time `yaml:"updated,omitempty"`
	Source     string    `yaml:"source,omitempty"`
}

func NewStore(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, fmt.Errorf("memory: store dir cannot be empty")
	}
	s := &Store{dir: opts.Dir, opts: opts}
	for _, d := range []string{s.dir, s.corePath(), s.entriesPath()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("memory: create %s: %w", d, err)
		}
	}
	for _, name := range []string{projectFile, preferencesFile} {
		p := filepath.Join(s.corePath(), name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o644); err != nil {
				return nil, fmt.Errorf("memory: seed %s: %w", name, err)
			}
		}
	}
	return s, nil
}

func (s *Store) Options() Options { return s.opts }

func (s *Store) corePath() string    { return filepath.Join(s.dir, coreDir) }
func (s *Store) entriesPath() string { return filepath.Join(s.dir, entriesDir) }

func (s *Store) Core() ([]Entry, error) {
	var out []Entry
	for _, name := range []string{projectFile, preferencesFile} {
		body, err := os.ReadFile(filepath.Join(s.corePath(), name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("memory: read core %s: %w", name, err)
		}
		text := strings.TrimSpace(string(body))
		if text == "" {
			continue
		}
		kind := KindProjectFact
		if name == preferencesFile {
			kind = KindPreference
		}
		out = append(out, Entry{
			ID:    coreDir + "/" + strings.TrimSuffix(name, ".md"),
			Kind:  kind,
			Title: strings.TrimSuffix(name, ".md"),
			Body:  text,
		})
	}
	return out, nil
}

func (s *Store) RepoMap() (string, error) {
	body, err := os.ReadFile(filepath.Join(s.dir, repoMapFile))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("memory: read repo map: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func (s *Store) WriteRepoMap(content string) error {
	return writeFileAtomic(filepath.Join(s.dir, repoMapFile), []byte(strings.TrimSpace(content)+"\n"), 0o644)
}

func (s *Store) Load() ([]Entry, error) {
	paths, err := filepath.Glob(filepath.Join(s.entriesPath(), "*.md"))
	if err != nil {
		return nil, fmt.Errorf("memory: list entries: %w", err)
	}
	out := make([]Entry, 0, len(paths))
	for _, p := range paths {
		e, err := loadEntry(p)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func (s *Store) Add(e Entry) error {
	now := time.Now()
	if e.Created.IsZero() {
		e.Created = now
	}
	e.Updated = now
	if strings.TrimSpace(e.ID) == "" {
		e.ID = s.uniqueID(slug(e.Title))
	}
	if strings.TrimSpace(e.Title) == "" {
		e.Title = e.ID
	}
	if e.Kind == "" {
		e.Kind = KindProjectFact
	}
	return s.write(e)
}

func (s *Store) Delete(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("memory: delete needs an id")
	}
	p := filepath.Join(s.entriesPath(), id+".md")
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("memory: delete %s: %w", id, err)
	}
	return nil
}

func (s *Store) Search(query string, limit int) ([]Entry, error) {
	entries, err := s.Load()
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > len(entries) {
		limit = len(entries)
	}
	terms := tokenize(query)
	type scored struct {
		entry Entry
		score float64
	}
	ranked := make([]scored, 0, len(entries))
	for _, e := range entries {
		sc := scoreEntry(terms, e)
		if len(terms) > 0 && sc == 0 {
			continue
		}
		ranked = append(ranked, scored{entry: e, score: sc})
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].entry.Updated.After(ranked[j].entry.Updated)
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]Entry, len(ranked))
	for i, r := range ranked {
		out[i] = r.entry
	}
	return out, nil
}

func (s *Store) write(e Entry) error {
	fm := frontmatter{
		ID:         e.ID,
		Kind:       string(e.Kind),
		Title:      e.Title,
		Tags:       e.Tags,
		Importance: e.Importance,
		Created:    e.Created,
		Updated:    e.Updated,
		Source:     e.Source,
	}
	head, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("memory: marshal %s: %w", e.ID, err)
	}
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(head)
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(e.Body))
	b.WriteString("\n")
	return writeFileAtomic(filepath.Join(s.entriesPath(), e.ID+".md"), []byte(b.String()), 0o644)
}

func loadEntry(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, fmt.Errorf("memory: read %s: %w", path, err)
	}
	id := strings.TrimSuffix(filepath.Base(path), ".md")
	fm, body := splitFrontmatter(string(data))
	e := Entry{
		ID:         fm.ID,
		Kind:       Kind(fm.Kind),
		Title:      fm.Title,
		Body:       body,
		Tags:       fm.Tags,
		Importance: fm.Importance,
		Created:    fm.Created,
		Updated:    fm.Updated,
		Source:     fm.Source,
	}
	if e.ID == "" {
		e.ID = id
	}
	if e.Title == "" {
		e.Title = e.ID
	}
	if e.Kind == "" {
		e.Kind = KindProjectFact
	}
	return e, nil
}

func splitFrontmatter(data string) (frontmatter, string) {
	var fm frontmatter
	rest, ok := strings.CutPrefix(data, "---\n")
	if !ok {
		return fm, strings.TrimSpace(data)
	}
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return fm, strings.TrimSpace(data)
	}
	_ = yaml.Unmarshal([]byte(rest[:idx]), &fm)
	body := strings.TrimPrefix(rest[idx+len("\n---"):], "\n")
	return fm, strings.TrimSpace(body)
}

func (s *Store) uniqueID(base string) string {
	if base == "" {
		base = "entry"
	}
	id := base
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(s.entriesPath(), id+".md")); os.IsNotExist(err) {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, i)
	}
}

func scoreEntry(terms []string, e Entry) float64 {
	if len(terms) == 0 {
		return float64(e.Importance) + 0.001
	}
	title := tokenSet(e.Title)
	tags := tokenSet(strings.Join(e.Tags, " "))
	body := tokenSet(e.Body)
	var s float64
	for _, t := range terms {
		if title[t] {
			s += 3
		}
		if tags[t] {
			s += 2
		}
		if body[t] {
			s += 1
		}
	}
	if s > 0 {
		s *= 1 + float64(e.Importance)
	}
	return s
}

func tokenSet(s string) map[string]bool {
	set := map[string]bool{}
	for _, t := range tokenize(s) {
		set[t] = true
	}
	return set
}

func tokenize(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

func slug(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = out[:60]
	}
	return out
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
