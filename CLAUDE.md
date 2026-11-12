# jobradar

Personal job intelligence platform: a Go core service (ingestion, tracking, REST API) and a
Python matching service, on PostgreSQL, run with Docker Compose.

## Commands
```sh
go build ./... && go vet ./...                     # Go 1.27, toolchain pinned in go.mod
go test -race ./...                                # CI; store tests need Docker (testcontainers)
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run   # CI
govulncheck ./...                                  # CI
go run ./cmd/jobradar migrate up|down|status       # needs DATABASE_URL (see .env.example)
go run ./cmd/jobradar serve                        # :8080; /healthz, /readyz
scripts/pin-matcher.sh <commit>   # pin a matcher commit on its main; updates contracts/
git -C ../jobradar-matcher switch --detach "$(cat contracts/MATCHER_REF)"
```

## Layout
- `docs/SPEC.md`: the project specification
- `docs/ARCHITECTURE.md`: Phase 0 proposal (status Proposed until I approve; approved
  decisions move to `docs/adr/`)
- `cmd/jobradar/main.go`: the binary: `serve`, `migrate`; wiring only
- `internal/api/`: router (`NewHandler`), middleware (request ID, log, recover, deadline),
  `WriteProblem` for every error; `internal/config/`: env settings, all errors at once;
  `internal/store/`: pgx pool, goose migrator, `SchemaStatus`
- `migrations/`: goose SQL files `NNNNN_name.sql`, embedded in the binary; never edit an
  applied one, add a new file
- `contracts/`: the pinned matcher: `MATCHER_REF` (commit SHA, no tags yet),
  `MATCHER_VERSION` (its pyproject version), `matcher.openapi.yaml` (its contract). Only
  `scripts/pin-matcher.sh` writes them; never edit by hand.
- `docs/adr/`: backend and cross-service ADRs (`NNNN-title.adr`), index in its README;
  matcher-internal ADRs live in `jobradar-matcher/docs/adr/`

## Conventions
- Phase 0 first: architecture proposal and ADRs, approved by me, before any implementation
  code (SPEC §14). I own every significant architectural decision.
- One ADR, one PR: the ADR and its implementation; lower numbers first. Commits start with
  `[ADR-NNNN]` (`workflow:commit-style`).
- No LLM API dependency, no extra infrastructure, no frontend without a stated need (SPEC §13).
- No committed secrets: configuration comes from the environment; `.env*` is ignored.
- Errors to clients are problem+json and never carry internal error text (DB errors,
  panics): log the detail with the `request_id`, return a short title.
- Pass `r.Context()` to every DB/HTTP call: the request deadline relies on it.
- sqlc arrives with the first tables (ADR 0005); CI then adds `sqlc diff`.
- Candidate data is personal: never hardcode it in the matching engine, never log it.
