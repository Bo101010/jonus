// Package cli implements the jonus command tree.
//
// Commands in this package are thin adapters: they parse flags into a request,
// call into the shared service layer, and render the result. No generation,
// queueing, or persistence logic belongs here — the web API is the other
// transport over the same core, and the two must never diverge.
package cli

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/Bo101010/jonus/internal/app"
	"github.com/Bo101010/jonus/internal/config"
)

const longDescription = `jonus is a local AI music generation studio.

It turns a text description into a finished song, and exposes the same engine
through two surfaces: this command line and a web UI served from localhost.

Engines are pluggable. The default "mock" engine synthesises audio locally with
no model installed, so the full workflow is usable immediately. "ace-step"
drives a local ACE-Step model, and "cloud" forwards to a hosted music API.`

// Execute runs the jonus command tree against ctx and returns the first error
// encountered. ctx is cancelled on SIGINT/SIGTERM by the caller.
func Execute(ctx context.Context) error {
	return newRootCmd().ExecuteContext(ctx)
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "jonus",
		Short:         "Local AI music generation with a CLI and a web UI",
		Long:          longDescription,
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	pf := cmd.PersistentFlags()
	pf.String("data-dir", "", "directory for the database and generated audio (default: the per-user data directory)")
	pf.String("engine", "", "generation engine: mock, ace-step, or cloud (default: mock)")
	pf.String("log-level", "", "log verbosity: debug, info, warn, or error (default: info)")

	cmd.AddCommand(
		newVersionCmd(),
		newDoctorCmd(),
		newServeCmd(),
		newGenerateCmd(),
		newJobsCmd(),
		newLibraryCmd(),
	)
	return cmd
}

// resolveConfig loads configuration from the environment and then applies any
// persistent flags the user actually set, so flags always take precedence.
func resolveConfig(cmd *cobra.Command) (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, err
	}

	flags := cmd.Flags()
	if flags.Changed("data-dir") {
		dir, err := flags.GetString("data-dir")
		if err != nil {
			return config.Config{}, fmt.Errorf("read --data-dir: %w", err)
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return config.Config{}, fmt.Errorf("resolve --data-dir: %w", err)
		}
		cfg.DataDir = abs
		cfg.DBPath = filepath.Join(abs, "jonus.db")
	}
	if flags.Changed("engine") {
		name, err := flags.GetString("engine")
		if err != nil {
			return config.Config{}, fmt.Errorf("read --engine: %w", err)
		}
		if err := cfg.SetEngine(name); err != nil {
			return config.Config{}, err
		}
	}
	if flags.Changed("log-level") {
		level, err := flags.GetString("log-level")
		if err != nil {
			return config.Config{}, fmt.Errorf("read --log-level: %w", err)
		}
		parsed, err := config.ParseLevel(level)
		if err != nil {
			return config.Config{}, err
		}
		cfg.LogLevel = parsed
	}

	return cfg, nil
}

// newLogger builds the process logger. Diagnostics go to stderr so that
// command output on stdout stays machine-readable.
func newLogger(cfg config.Config) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: cfg.LogLevel,
	}))
}

// withApp opens the application for the duration of fn and closes it after.
//
// Commands that only read or mutate existing state use this. Commands that must
// keep the application alive beyond a single call — generate --wait, which
// follows a job the running process itself has to advance — build the App
// themselves instead.
func withApp(cmd *cobra.Command, fn func(*app.App) error) error {
	cfg, err := resolveConfig(cmd)
	if err != nil {
		return err
	}

	a, err := app.New(cmd.Context(), &cfg, newLogger(cfg))
	if err != nil {
		return err
	}
	defer func() {
		if err := a.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "closing the application: %v\n", err)
		}
	}()

	return fn(a)
}
