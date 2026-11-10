// Package config resolves the runtime configuration for a jonus process.
//
// Configuration is layered: built-in defaults, then environment variables,
// then CLI flags. Load returns the result of the first two layers; the CLI
// applies flag overrides on top of the returned value so that flags always
// win.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Engine names accepted by Config.EngineName.
const (
	// EngineMock synthesises audio locally without any ML model. It is the
	// default so that the application is usable before any model is installed.
	EngineMock = "mock"

	// EngineACEStep drives the ACE-Step 1.5 Python sidecar over HTTP.
	EngineACEStep = "ace-step"

	// EngineCloud drives a third-party music generation API.
	EngineCloud = "cloud"
)

// DefaultPort is the port the local HTTP server listens on.
const DefaultPort = 7777

// Config is the resolved configuration for one jonus process.
type Config struct {
	// DataDir holds the database, generated audio, and cached waveforms.
	DataDir string

	// DBPath is the SQLite database file.
	DBPath string

	// HTTPAddr is the listen address for the web UI and API.
	HTTPAddr string

	// EngineName selects the generation engine.
	EngineName string

	// LogLevel is the minimum level written to the log.
	LogLevel slog.Level

	// Dev enables development behaviour: relaxed CORS and verbose logging.
	Dev bool

	// Sidecar configures the Python generation backend. It is only used when
	// EngineName is EngineACEStep.
	Sidecar Sidecar

	// Cloud configures the hosted generation backend. It is only used when
	// EngineName is EngineCloud.
	Cloud Cloud
}

// Cloud configures a hosted music generation API.
//
// Every field is optional except APIKey. Defaults are applied by the cloud
// engine rather than here, so that the vendor's own endpoints are named in one
// place — the package that talks to them.
type Cloud struct {
	// Provider selects the vendor: "minimax" or "mureka".
	Provider string

	// BaseURL is the API root, without a trailing slash.
	BaseURL string

	// APIKey authenticates the request.
	APIKey string

	// Model overrides the provider's default model.
	Model string

	// Timeout bounds one generation request, which includes the time the
	// vendor spends writing and rendering the song.
	Timeout time.Duration
}

// Sidecar configures the supervised Python generation backend.
type Sidecar struct {
	// Dir is the working directory of the Python project.
	Dir string

	// Command and Args launch the sidecar's HTTP server.
	Command string
	Args    []string

	// BaseURL is the sidecar's HTTP endpoint once it is listening.
	BaseURL string

	// ReadyTimeout bounds how long the model may take to load. Loading a
	// diffusion model on a cold cache can take minutes, so this is generous.
	ReadyTimeout time.Duration

	// HealthInterval is how often the sidecar is polled once ready.
	HealthInterval time.Duration

	// ShutdownGrace is how long the process group is given to exit after
	// SIGTERM before it is killed.
	ShutdownGrace time.Duration

	// MaxRestarts bounds automatic restarts before the manager circuit-breaks.
	MaxRestarts int
}

// Load resolves configuration from environment variables, falling back to
// per-user defaults. It returns an error only when a value is present but
// unparseable, or when the data directory cannot be prepared.
func Load() (Config, error) {
	dataDir, err := defaultDataDir()
	if err != nil {
		return Config{}, err
	}
	if v := os.Getenv("JONUS_DATA_DIR"); v != "" {
		dataDir = v
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		return Config{}, fmt.Errorf("resolve data dir: %w", err)
	}

	cfg := Config{
		DataDir:    dataDir,
		DBPath:     filepath.Join(dataDir, "jonus.db"),
		HTTPAddr:   fmt.Sprintf("127.0.0.1:%d", DefaultPort),
		EngineName: EngineMock,
		LogLevel:   slog.LevelInfo,
		Sidecar: Sidecar{
			Dir:            filepath.Join(repoRoot(), "python"),
			Command:        "uv",
			Args:           []string{"run", "acestep-api"},
			BaseURL:        "http://127.0.0.1:8001",
			ReadyTimeout:   10 * time.Minute,
			HealthInterval: 15 * time.Second,
			ShutdownGrace:  20 * time.Second,
			MaxRestarts:    5,
		},
	}

	if v := os.Getenv("JONUS_HTTP_ADDR"); v != "" {
		cfg.HTTPAddr = v
	}
	if v := os.Getenv("JONUS_LOG_LEVEL"); v != "" {
		lvl, err := ParseLevel(v)
		if err != nil {
			return Config{}, err
		}
		cfg.LogLevel = lvl
	}
	if v := os.Getenv("JONUS_SIDECAR_URL"); v != "" {
		cfg.Sidecar.BaseURL = v
	}
	if v := os.Getenv("JONUS_SIDECAR_DIR"); v != "" {
		cfg.Sidecar.Dir = v
	}
	if err := cfg.loadCloud(); err != nil {
		return Config{}, err
	}

	// The engine is chosen last, because one of its inputs is the cloud key.
	switch v := os.Getenv("JONUS_ENGINE"); {
	case v != "":
		if err := cfg.setEngine(v); err != nil {
			return Config{}, err
		}
	case cfg.Cloud.APIKey != "":
		// An API key is only ever set deliberately, and its only use is the
		// cloud engine. Answering a configured installation with a synthetic
		// sine wave would read as a bug.
		cfg.EngineName = EngineCloud
	}

	return cfg, nil
}

// loadCloud reads the cloud engine's settings.
func (c *Config) loadCloud() error {
	if v := os.Getenv("JONUS_CLOUD_PROVIDER"); v != "" {
		c.Cloud.Provider = v
	}
	if v := os.Getenv("JONUS_CLOUD_BASE_URL"); v != "" {
		c.Cloud.BaseURL = v
	}
	if v := os.Getenv("JONUS_CLOUD_API_KEY"); v != "" {
		c.Cloud.APIKey = v
	}
	if v := os.Getenv("JONUS_CLOUD_MODEL"); v != "" {
		c.Cloud.Model = v
	}
	if v := os.Getenv("JONUS_CLOUD_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("JONUS_CLOUD_TIMEOUT: %w", err)
		}
		c.Cloud.Timeout = d
	}
	return nil
}

// SetEngine validates and sets the engine name.
func (c *Config) SetEngine(name string) error { return c.setEngine(name) }

func (c *Config) setEngine(name string) error {
	switch name {
	case EngineMock, EngineACEStep, EngineCloud:
		c.EngineName = name
		return nil
	default:
		return fmt.Errorf("unknown engine %q (want one of: %s, %s, %s)",
			name, EngineMock, EngineACEStep, EngineCloud)
	}
}

// defaultDataDir returns the per-user directory jonus stores state in.
func defaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(base, "jonus"), nil
}

// repoRoot best-effort locates the project root so that the Python sidecar can
// be found during development. It falls back to the working directory, which is
// the right answer when jonus runs from a checkout.
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// ParseLevel converts a log level name into a slog.Level.
func ParseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown log level %q (want debug, info, warn, or error)", s)
	}
}

// ParsePort extracts the port from a listen address such as "127.0.0.1:7777".
func ParsePort(addr string) (int, error) {
	_, portStr, ok := strings.Cut(addr, ":")
	if !ok {
		return 0, fmt.Errorf("address %q has no port", addr)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("address %q has an invalid port: %w", addr, err)
	}
	return port, nil
}
