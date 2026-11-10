package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/shaolianbo/jonus/internal/app"
	"github.com/shaolianbo/jonus/internal/config"
	"github.com/shaolianbo/jonus/internal/httpapi"
	"github.com/shaolianbo/jonus/internal/webui"
)

// Server timing. The read timeout is generous because a request may wait on the
// database, but there is no write timeout: the event streams are long-lived
// responses that would be cut off by one.
const (
	readHeaderTimeout = 10 * time.Second
	shutdownTimeout   = 10 * time.Second
)

func newServeCmd() *cobra.Command {
	var (
		port int
		open bool
		dev  bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the web UI and HTTP API",
		Long: `Run the web UI and HTTP API.

The server starts immediately and serves the interface even while a generation
engine is still loading, so a slow model never blocks access to the library.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := resolveConfig(cmd)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("port") {
				cfg.HTTPAddr = fmt.Sprintf("127.0.0.1:%d", port)
			}
			cfg.Dev = dev

			log := newLogger(cfg)

			a, err := app.New(cmd.Context(), &cfg, log)
			if err != nil {
				return err
			}
			defer func() {
				if err := a.Close(); err != nil {
					log.Error("shutting down", "error", err)
				}
			}()

			ui, err := webui.NewHandler()
			if err != nil {
				return err
			}

			server := httpapi.New(a, log, ui, httpapi.WithDev(dev))
			httpServer := &http.Server{
				Addr:              cfg.HTTPAddr,
				Handler:           server.Handler(),
				ReadHeaderTimeout: readHeaderTimeout,
			}

			// Fail before printing a URL that does not work: binding happens
			// here rather than inside ListenAndServe, which reports the error
			// only after the goroutine has started.
			listener, err := net.Listen("tcp", cfg.HTTPAddr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
			}

			url := "http://" + listener.Addr().String()
			fmt.Printf("jonus is running at %s\n", url)
			fmt.Printf("  engine  %s\n", cfg.EngineName)
			fmt.Printf("  data    %s\n", cfg.DataDir)
			if dev {
				fmt.Println("  dev mode: the Vite dev server on :5173 proxies /api here")
			}

			if open {
				if err := openBrowser(url); err != nil {
					log.Warn("could not open a browser", "error", err)
				}
			}

			return runServer(cmd.Context(), log, a, httpServer, listener)
		},
	}

	flags := cmd.Flags()
	flags.IntVar(&port, "port", 0, fmt.Sprintf("port to listen on (default %d)", config.DefaultPort))
	flags.BoolVar(&open, "open", false, "open the web UI in a browser once the server is listening")
	flags.BoolVar(&dev, "dev", false, "development mode: allow the Vite dev server to call the API")

	return cmd
}

// runServer runs the queue and the HTTP server together until ctx is cancelled
// or either of them fails.
//
// They are started as one group so that a failure in either stops the other:
// a server without a queue would accept jobs it never runs.
func runServer(
	ctx context.Context,
	log *slog.Logger,
	a *app.App,
	httpServer *http.Server,
	listener net.Listener,
) error {
	group, ctx := errgroup.WithContext(ctx)

	group.Go(func() error {
		return a.Run(ctx)
	})

	group.Go(func() error {
		err := httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})

	// Shutdown is driven by ctx, so a signal or a failure elsewhere drains the
	// server rather than leaving it serving.
	group.Go(func() error {
		<-ctx.Done()

		// Detached from ctx, which is already cancelled, so the drain gets a
		// chance to finish.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
		defer cancel()

		log.Info("shutting down")
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down http server: %w", err)
		}
		return nil
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// openBrowser asks the desktop to open url.
//
// Failure is not an error worth stopping for: the user can open the address
// themselves, and the server is already running.
func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}

	return exec.Command(cmd, append(args, url)...).Start()
}
