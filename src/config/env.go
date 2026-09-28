package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const DefaultModel = "z-ai/glm-5.3-flash"

// DefaultTheme is used when no theme is configured. "tokyo" is a
// Tokyonight-inspired dark palette; "system" inherits the terminal's ANSI palette.
const DefaultTheme = "tokyo"

var (
	KnownProviders = []string{"openrouter"}
	KnownEnvs      = []string{"local", "docker"}
)

// legacyConfigPath is the pre-split config location. It is read once to migrate
// existing installs to config.yaml + .env.
func legacyConfigPath() string {
	return filepath.Join(ProjectDir, "stubbs.env")
}

// StubbsConfig is the persisted configuration. The API key lives in .env; every
// other field lives in config.yaml.
type StubbsConfig struct {
	Provider string   `yaml:"provider"`
	Models   []string `yaml:"models,omitempty"`
	Env      string   `yaml:"env"`
	Theme    string   `yaml:"theme"`
	APIKey   string   `yaml:"-"`
}

// ActiveModel returns the most recently selected model, falling back to the
// built-in default when nothing has been selected yet.
func (c StubbsConfig) ActiveModel() string {
	if len(c.Models) == 0 {
		return DefaultModel
	}
	return c.Models[0]
}

// SetActiveModel records model as the most recently selected model, keeping the
// list in LIFO order (most recent first) without duplicates.
func (c *StubbsConfig) SetActiveModel(model string) {
	c.Models = withModel(c.Models, model)
}

// withModel returns models with model moved to the front (LIFO), dropping any
// earlier occurrence so the list never repeats.
func withModel(models []string, model string) []string {
	if model == "" {
		return models
	}
	out := make([]string, 0, len(models)+1)
	out = append(out, model)
	for _, m := range models {
		if m != "" && m != model {
			out = append(out, m)
		}
	}
	return out
}

func defaultConfig() StubbsConfig {
	return StubbsConfig{
		Provider: "openrouter",
		Env:      "local",
		Theme:    DefaultTheme,
	}
}

// readConfigFile parses config.yaml without applying defaults or environment
// overrides. It also understands the legacy scalar `model:` key.
func readConfigFile() StubbsConfig {
	var cfg StubbsConfig
	b, err := os.ReadFile(ProjectConfigFile)
	if err != nil {
		return cfg
	}
	var fileCfg struct {
		Provider string   `yaml:"provider"`
		Model    string   `yaml:"model"` // legacy scalar
		Models   []string `yaml:"models"`
		Env      string   `yaml:"env"`
		Theme    string   `yaml:"theme"`
	}
	if err := yaml.Unmarshal(b, &fileCfg); err != nil {
		return cfg
	}
	cfg.Provider = fileCfg.Provider
	cfg.Env = fileCfg.Env
	cfg.Theme = fileCfg.Theme
	if len(fileCfg.Models) > 0 {
		cfg.Models = fileCfg.Models
	} else if fileCfg.Model != "" {
		cfg.Models = []string{fileCfg.Model}
	}
	return cfg
}

// fileOrDefaults merges config.yaml over the built-in defaults, ignoring the
// process environment so callers can persist without leaking env-only values.
func fileOrDefaults() StubbsConfig {
	cfg := defaultConfig()
	fileCfg := readConfigFile()
	if fileCfg.Provider != "" {
		cfg.Provider = fileCfg.Provider
	}
	if len(fileCfg.Models) > 0 {
		cfg.Models = fileCfg.Models
	}
	if fileCfg.Env != "" {
		cfg.Env = fileCfg.Env
	}
	if fileCfg.Theme != "" {
		cfg.Theme = fileCfg.Theme
	}
	return cfg
}

func Load() StubbsConfig {
	migrateLegacy()

	cfg := fileOrDefaults()

	envFile := parseEnvFile(ProjectEnvFile)
	if v := firstNonEmpty(envFile["API_KEY"], envFile["STUBBS_API_KEY"]); v != "" {
		cfg.APIKey = v
	}

	if v := os.Getenv("STUBBS_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("STUBBS_MODEL"); v != "" {
		cfg.SetActiveModel(v)
	}
	if v := os.Getenv("STUBBS_ENV"); v != "" {
		cfg.Env = v
	}
	if v := os.Getenv("STUBBS_THEME"); v != "" {
		cfg.Theme = v
	}
	if v := os.Getenv("STUBBS_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		cfg.APIKey = v
	}

	return cfg
}

// IsConfigured reports whether stubbs has usable settings: config.yaml contains
// settings, .env contains an API key, or the environment provides an API key
// (STUBBS_API_KEY / OPENROUTER_API_KEY), which Load() honors. Benchmark and
// container runs configure via env, so they must not trigger the wizard.
func IsConfigured() bool {
	migrateLegacy()

	if configFileHasSettings() {
		return true
	}
	if len(parseEnvFile(ProjectEnvFile)) > 0 {
		return true
	}
	return os.Getenv("STUBBS_API_KEY") != "" || os.Getenv("OPENROUTER_API_KEY") != ""
}

// SaveConfig writes the non-secret settings to config.yaml and, when set, the
// API key to .env. Both writes are atomic.
func SaveConfig(cfg StubbsConfig) error {
	if err := writeYAML(ProjectConfigFile, cfg); err != nil {
		return err
	}
	if cfg.APIKey != "" {
		if err := writeEnvKey(ProjectEnvFile, "API_KEY", cfg.APIKey); err != nil {
			return err
		}
	}
	return nil
}

// SaveModel records model as the most recently selected model in config.yaml
// (LIFO), preserving the other non-secret settings. Secrets are never written.
func SaveModel(model string) error {
	cfg := fileOrDefaults()
	cfg.SetActiveModel(model)
	return writeYAML(ProjectConfigFile, cfg)
}

// migrateLegacy converts a pre-split .stubbs/stubbs.env into config.yaml + .env.
// It runs only when config.yaml does not exist yet, so it is safe to call from
// both Load and IsConfigured.
func migrateLegacy() {
	if _, err := os.Stat(ProjectConfigFile); err == nil {
		return
	}
	legacy := legacyConfigPath()
	old := parseEnvFile(legacy)
	if len(old) == 0 {
		return
	}

	cfg := defaultConfig()
	if v := old["PROVIDER"]; v != "" {
		cfg.Provider = v
	}
	if v := firstNonEmpty(old["MODEL"], old["STUBBS_MODEL"]); v != "" {
		cfg.Models = []string{v}
	}
	if v := old["ENV"]; v != "" {
		cfg.Env = v
	}
	if v := old["STUBBS_THEME"]; v != "" {
		cfg.Theme = v
	}
	if v := firstNonEmpty(old["API_KEY"], old["STUBBS_API_KEY"]); v != "" {
		cfg.APIKey = v
	}

	if err := SaveConfig(cfg); err != nil {
		return
	}
	// The old file is only a migration source; remove it so it cannot drift
	// out of sync with the new files.
	_ = os.Remove(legacy)
}

func configFileHasSettings() bool {
	c := readConfigFile()
	return c.Provider != "" || len(c.Models) > 0 || c.Env != "" || c.Theme != ""
}

func writeYAML(path string, cfg StubbsConfig) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	out.WriteString("# stubbs configuration. Secrets live in .env.\n")
	out.Write(b)
	return writeFileAtomic(path, out.Bytes(), 0o644)
}

// writeEnvKey updates a single KEY=VALUE in an env-style file, preserving other
// keys. The file is written 0o600 because it may hold a secret.
func writeEnvKey(path, key, value string) error {
	merged := parseEnvFile(path)
	merged[key] = value

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, merged[k])
	}
	return writeFileAtomic(path, []byte(b.String()), 0o600)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if len(val) >= 2 && (val[0] == '"' && val[len(val)-1] == '"' || val[0] == '\'' && val[len(val)-1] == '\'') {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
