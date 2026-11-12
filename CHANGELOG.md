# Changelog

## Unreleased

### Fixed
- Syncs could deadlock when as many ran at once as the pool had connections; each sync now uses one connection, and manual syncs share the concurrency limit
- The API refuses foreign Host headers and cross-site Origins, so web pages in your browser cannot call it (DNS rebinding, CSRF); `ALLOWED_HOSTS` sets the allowed names
- Empty source and run lists are `[]` instead of `null`

### Added
- Project spec and Phase 0 architecture proposal for review
- ADRs 0001–0009 (Proposed): repos, Go stack, compose, matcher contract, ingestion, profiles, model versions, applications, observability
- ADR 0010 (Proposed): model lifecycle in MLflow, replacing the rejected 0007
- Git-ignore real profiles, exported labels and compose overrides
- Go backend skeleton: `jobradar serve` (`/healthz`, `/readyz` with schema check) and `migrate up|down|status`, env config, JSON logs with request IDs, problem+json errors, goose migrations, CI
- Local deployment: `compose.yaml` with Postgres 18, migrations, backend, matcher and MLflow 3.18, health checks, `jobradar healthcheck`, backup and restore in the README
- Job ingestion: sources API, scheduled and manual syncs with retries and rate limits, idempotent upserts, closing of vanished postings, cross-source dedup, Greenhouse adapter

### Changed
- ADR 0010: MLflow UI on host port 5001, psycopg 3, `--allowed-hosts` (as built in the compose PR)
- CI workflow disabled until the coding phases are done; checks run locally
- ADR 0001: the backend pins a matcher commit SHA instead of a tag; `scripts/pin-matcher.sh` and `contracts/`
- ADR 0010 accepted; ADR 0003 updated for the MLflow service and volumes
