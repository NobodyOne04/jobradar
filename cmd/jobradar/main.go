// Command jobradar is the backend: `serve` runs the API, `migrate up|down|status` the schema.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NobodyOne04/jobradar/internal/api"
	"github.com/NobodyOne04/jobradar/internal/config"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const usage = "usage: jobradar serve | migrate up|down|status"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "jobradar:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("service", "backend")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	switch args[0] {
	case "serve":
		return serve(ctx, cfg, log, st)
	case "migrate":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return migrate(ctx, log, st, args[1])
	default:
		return errors.New(usage)
	}
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger, st *store.Store) error {
	ready := func(ctx context.Context) error {
		if err := st.Pool.Ping(ctx); err != nil {
			log.Warn("database ping failed", "error", err)
			return errors.New("database unreachable") // no connection details to clients
		}
		current, latest, err := st.SchemaStatus(ctx)
		if err != nil {
			log.Warn("schema status failed", "error", err)
			return errors.New("schema version unknown")
		}
		if current != latest {
			return fmt.Errorf("schema at version %d, binary expects %d: run `jobradar migrate up`", current, latest)
		}
		return nil
	}

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.NewHandler(log, ready, cfg.RequestTimeout),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      cfg.RequestTimeout + 5*time.Second, // backstop behind the per-request deadline
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.HTTPAddr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

func migrate(ctx context.Context, log *slog.Logger, st *store.Store, cmd string) error {
	p, err := st.Migrator()
	if err != nil {
		return err
	}
	switch cmd {
	case "up":
		results, err := p.Up(ctx)
		for _, r := range results {
			log.Info("migrated up", "version", r.Source.Version, "file", r.Source.Path, "duration_ms", r.Duration.Milliseconds())
		}
		return err
	case "down":
		r, err := p.Down(ctx)
		if r != nil {
			log.Info("migrated down", "version", r.Source.Version, "file", r.Source.Path)
		}
		return err
	case "status":
		statuses, err := p.Status(ctx)
		for _, s := range statuses {
			fmt.Printf("%-8d %-10s %s\n", s.Source.Version, s.State, s.Source.Path)
		}
		return err
	default:
		return errors.New(usage)
	}
}
