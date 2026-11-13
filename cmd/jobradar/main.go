// Command jobradar is the backend: `serve` runs the API, `migrate up|down|status` the schema,
// `healthcheck` probes /readyz for Docker (the distroless image has no shell or curl).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/NobodyOne04/jobradar/internal/api"
	"github.com/NobodyOne04/jobradar/internal/config"
	"github.com/NobodyOne04/jobradar/internal/ingest"
	"github.com/NobodyOne04/jobradar/internal/ingest/ashby"
	"github.com/NobodyOne04/jobradar/internal/ingest/greenhouse"
	"github.com/NobodyOne04/jobradar/internal/ingest/himalayas"
	"github.com/NobodyOne04/jobradar/internal/ingest/lever"
	"github.com/NobodyOne04/jobradar/internal/profiles"
	"github.com/NobodyOne04/jobradar/internal/store"
)

const usage = "usage: jobradar serve | migrate up|down|status | healthcheck | profile import <file.yaml>"

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
	if args[0] == "healthcheck" {
		return healthcheck(os.Getenv("HTTP_ADDR"))
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})).
		With("service", "backend")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Each sync holds one connection; the rest serve the API and the scheduler's queries.
	st, err := store.Open(ctx, cfg.DatabaseURL, int32(cfg.SyncConcurrency)+10)
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
	case "profile":
		if len(args) != 3 || args[1] != "import" {
			return errors.New(usage)
		}
		return importProfile(ctx, log, st, args[2])
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

	queries := store.New(st.Pool)
	// shortcut: assumes one backend process; with several, a live peer's runs would be
	// marked failed too. Upgrade by recording the owning process on each run.
	if n, err := queries.FailInterruptedRuns(ctx); err != nil {
		log.Warn("could not close interrupted sync runs", "error", err)
	} else if n > 0 {
		log.Info("closed interrupted sync runs", "count", n)
	}

	httpClient := ingest.NewHTTPClient(2) // per source kind: at most 2 requests a second
	scheduler := &ingest.Scheduler{
		Syncer: &ingest.Syncer{
			Pool: st.Pool,
			Adapters: ingest.NewRegistry(greenhouse.New(httpClient), lever.New(httpClient),
				ashby.New(httpClient), himalayas.New()),
			Log: log,
		},
		Interval:    cfg.SyncInterval,
		Concurrency: cfg.SyncConcurrency,
		Tick:        time.Minute,
		Log:         log,
	}
	sources := &api.Sources{Queries: queries, Scheduler: scheduler, Log: log, Background: ctx}
	profileAPI := &api.Profiles{Queries: queries, Service: &profiles.Service{Pool: st.Pool}, Log: log}

	var background sync.WaitGroup
	background.Go(func() { scheduler.Run(ctx) })

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: api.NewHandler(log, ready, cfg.RequestTimeout, cfg.AllowedHosts,
			sources.Register, profileAPI.Register),
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
	err := srv.Shutdown(shutdownCtx)
	// ctx is cancelled: running syncs stop at the next posting and record themselves failed.
	background.Wait()
	sources.Wait()
	return err
}

func migrate(ctx context.Context, log *slog.Logger, st *store.Store, cmd string) error {
	p, err := st.Migrator()
	if err != nil {
		return err
	}
	switch cmd {
	case "up":
		results, err := p.Up(ctx)
		if err == nil && len(results) == 0 {
			log.Info("schema up to date")
		}
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

// healthcheck needs no DATABASE_URL: it only asks the running server whether it is ready.
func healthcheck(addr string) error {
	if addr == "" {
		addr = ":8080"
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("HTTP_ADDR %q: %w", addr, err)
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", resp.Status)
	}
	return nil
}

// importProfile adds the YAML file as a revision of the profile named after the file
// (profiles/anna.yaml -> "anna"). Only ids and revision numbers are printed, never content.
func importProfile(ctx context.Context, log *slog.Logger, st *store.Store, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	doc, err := profiles.FromYAML(src)
	if err != nil {
		return err
	}
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	res, err := (&profiles.Service{Pool: st.Pool}).Import(ctx, name, doc)
	if err != nil {
		return err
	}
	if res.Created {
		log.Info("profile revision created", "profile", name, "profile_id", res.Revision.ProfileID,
			"revision", res.Revision.Revision)
	} else {
		log.Info("profile unchanged", "profile", name, "profile_id", res.Revision.ProfileID,
			"revision", res.Revision.Revision)
	}
	return nil
}
