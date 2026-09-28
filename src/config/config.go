package config

import (
	"fmt"
	"os"
	"path/filepath"
)

var DEBUG = true

var (
	ProjectDir        string
	SessionsDir       string
	ContextDir        string
	MemoryDir         string
	ProjectConfigFile string // config.yaml: everything except secrets
	ProjectEnvFile    string // .env: the API key
)

func init() {
	var homeDir string
	var err error

	if DEBUG {
		homeDir, err = os.Getwd()
	} else {
		homeDir, err = os.UserHomeDir()
	}
	ProjectDir = filepath.Join(homeDir, ".stubbs")
	if err := os.MkdirAll(ProjectDir, os.ModePerm); err != nil {
		panic(fmt.Errorf("create users dir: %w", err))
	}

	ProjectConfigFile = filepath.Join(ProjectDir, "config.yaml")
	ProjectEnvFile = filepath.Join(ProjectDir, ".env")

	SessionsDir = filepath.Join(ProjectDir, "sessions")
	if err := os.MkdirAll(SessionsDir, os.ModePerm); err != nil {
		panic(fmt.Errorf("create sessions dir: %w", err))
	}

	ContextDir = filepath.Join(ProjectDir, "context")
	if err := os.MkdirAll(ContextDir, os.ModePerm); err != nil {
		panic(fmt.Errorf("create context dir: %w", err))
	}

	MemoryDir = filepath.Join(ProjectDir, "memory")
	if err := os.MkdirAll(MemoryDir, os.ModePerm); err != nil {
		panic(fmt.Errorf("create memory dir: %w", err))
	}

	if err != nil {
		panic(err)
	}
}
