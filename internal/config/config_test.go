package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDefaults(t *testing.T) {
	c, err := parse(env(map[string]string{"DATABASE_URL": "postgres://x"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.HTTPAddr != ":8080" || c.LogLevel != slog.LevelInfo ||
		c.RequestTimeout != 30*time.Second || c.ShutdownTimeout != 10*time.Second {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestReportsEveryError(t *testing.T) {
	_, err := parse(env(map[string]string{
		"LOG_LEVEL":       "loud",
		"REQUEST_TIMEOUT": "-1s",
	}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"DATABASE_URL", "LOG_LEVEL", "REQUEST_TIMEOUT"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}
