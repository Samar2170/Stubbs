package config

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"
)

const DefaultModel = "z-ai/glm-5.3-flash"

var (
	KnownProviders = []string{"openrouter"}
	KnownEnvs      = []string{"local", "docker"}
)

type StubbsConfig struct {
	Provider string
	Model    string
	Env      string
	APIKey   string
}

func Load() StubbsConfig {
	file := parseEnvFile(ProjectConfigFile)

	cfg := StubbsConfig{
		Provider: "openrouter",
		Model:    DefaultModel,
		Env:      "local",
	}

	if v := file["PROVIDER"]; v != "" {
		cfg.Provider = v
	}
	if v := file["STUBBS_MODEL"]; v != "" {
		cfg.Model = v
	}
	if v := file["MODEL"]; v != "" {
		cfg.Model = v
	}
	if v := file["ENV"]; v != "" {
		cfg.Env = v
	}
	if v := file["API_KEY"]; v != "" {
		cfg.APIKey = v
	}
	if v := file["STUBBS_API_KEY"]; v != "" {
		cfg.APIKey = v
	}

	if v := os.Getenv("STUBBS_PROVIDER"); v != "" {
		cfg.Provider = v
	}
	if v := os.Getenv("STUBBS_MODEL"); v != "" {
		cfg.Model = v
	}
	if v := os.Getenv("STUBBS_ENV"); v != "" {
		cfg.Env = v
	}
	if v := os.Getenv("STUBBS_API_KEY"); v != "" {
		cfg.APIKey = v
	}
	if v := os.Getenv("OPENROUTER_API_KEY"); v != "" {
		cfg.APIKey = v
	}

	return cfg
}

// IsConfigured reports whether ProjectConfigFile contains any settings. An
// empty (or missing) file means first-time setup should run.
func IsConfigured() bool {
	return len(parseEnvFile(ProjectConfigFile)) > 0
}

// SaveConfig merges values into the env file at path, preserving other keys.
// The file is written atomically.
func SaveConfig(path string, values map[string]string) error {
	merged := parseEnvFile(path)
	for k, v := range values {
		merged[k] = v
	}

	keys := make([]string, 0, len(merged))
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, merged[k])
	}

	tmp := path + ".tmp"
	// 0o600: the file may contain an API key.
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
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
