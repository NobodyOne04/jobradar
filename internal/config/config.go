// Package config reads the backend's settings from environment variables (ADR 0002).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the validated backend configuration.
type Config struct {
	DatabaseURL     string        // DATABASE_URL, required
	HTTPAddr        string        // HTTP_ADDR, default ":8080"
	LogLevel        slog.Level    // LOG_LEVEL: debug|info|warn|error, default info
	RequestTimeout  time.Duration // REQUEST_TIMEOUT, default 30s
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, default 10s
	SyncInterval    time.Duration // SYNC_INTERVAL: how old a source's last good sync may get, default 6h
	SyncConcurrency int           // SYNC_CONCURRENCY: sources synced at once, default 4
}

// FromEnv reads the environment and returns every invalid setting at once.
func FromEnv() (Config, error) {
	return parse(os.Getenv)
}

func parse(getenv func(string) string) (Config, error) {
	var errs []error
	get := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}
	duration := func(name, def string) time.Duration {
		raw := get(name, def)
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s must be a positive duration like 30s, got %q", name, raw))
		}
		return d
	}

	c := Config{
		DatabaseURL:     get("DATABASE_URL", ""),
		HTTPAddr:        get("HTTP_ADDR", ":8080"),
		RequestTimeout:  duration("REQUEST_TIMEOUT", "30s"),
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", "10s"),
		SyncInterval:    duration("SYNC_INTERVAL", "6h"),
	}
	if n, err := strconv.Atoi(get("SYNC_CONCURRENCY", "4")); err != nil || n < 1 || n > 32 {
		errs = append(errs, fmt.Errorf("SYNC_CONCURRENCY must be 1-32, got %q", get("SYNC_CONCURRENCY", "4")))
	} else {
		c.SyncConcurrency = n
	}
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL must be set"))
	}
	if level := get("LOG_LEVEL", "info"); c.LogLevel.UnmarshalText([]byte(level)) != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL must be debug, info, warn or error, got %q", level))
	}
	return c, errors.Join(errs...)
}
