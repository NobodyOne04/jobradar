# Changelog

## Unreleased

### Added
- Project spec and Phase 0 architecture proposal for review
- ADRs 0001–0009 (Proposed): repos, Go stack, compose, matcher contract, ingestion, profiles, model versions, applications, observability
- ADR 0010 (Proposed): model lifecycle in MLflow, replacing the rejected 0007
- Git-ignore real profiles, exported labels and compose overrides
- Go backend skeleton: `jobradar serve` (`/healthz`, `/readyz` with schema check) and `migrate up|down|status`, env config, JSON logs with request IDs, problem+json errors, goose migrations, CI
- Local deployment: `compose.yaml` with Postgres 18, migrations, backend, matcher and MLflow 3.18, health checks, `jobradar healthcheck`, backup and restore in the README

### Changed
- ADR 0001: the backend pins a matcher commit SHA instead of a tag; `scripts/pin-matcher.sh` and `contracts/`
- ADR 0010 accepted; ADR 0003 updated for the MLflow service and volumes
