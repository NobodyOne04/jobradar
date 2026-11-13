// Package contracts embeds the pinned matcher release (ADR 0001): its version, for the startup
// compatibility check, and its OpenAPI contract, for the contract tests. Only
// scripts/pin-matcher.sh writes these files.
package contracts

import (
	_ "embed"
	"strings"
)

//go:embed MATCHER_VERSION
var matcherVersion string

// MatcherVersion is the pinned matcher's package version, e.g. "0.2.0".
var MatcherVersion = strings.TrimSpace(matcherVersion)

// MatcherOpenAPI is the pinned matcher's openapi.yaml.
//
//go:embed matcher.openapi.yaml
var MatcherOpenAPI []byte
