# Changelog

## Unreleased

### Changed
- Source catalog `docs/sources.json`: 9 job-board APIs and 10 ATS platforms checked live, with their terms; 10 unsuitable ones listed as removed

### Fixed
- Teamtailor sources could reach internal addresses through a host name that resolves (or redirects) to one; its client now connects to public addresses only (ADR 0014)
- Jobs whose location lists ~200 countries could not be stored: dedup keys are now SHA-256 hashes (existing keys migrated)
- Syncs could deadlock when as many ran at once as the pool had connections; each sync now uses one connection, and manual syncs share the concurrency limit
- The API refuses foreign Host headers and cross-site Origins, so web pages in your browser cannot call it (DNS rebinding, CSRF); `ALLOWED_HOSTS` sets the allowed names
- Empty source and run lists are `[]` instead of `null`

### Added
- Company boards on SmartRecruiters, Workable, Recruitee, Personio, Breezy, Teamtailor and Rippling; descriptions fetched per job only when new or changed; board tokens validated; an empty first sync says "check board_token" (ADR 0014)
- Feed sources Jobicy, Remote OK, Remotive and Dev Global Jobs: saved feed filters, jobs close after 30 days unseen, "via <board>" attribution, Remotive at most every 6h and Jobicy hourly (manual syncs get 429 sooner) (ADR 0013)
- Web UI at http://localhost:3000 (repo `jobradar-web`, compose service `web`): job list with filters, job page with match details and status buttons (ADR 0012)
- Job search API: `/api/v1/jobs` with each job's match and application status, full-text search, filters, sort by score or date, keyset pages; `/api/v1/jobs/{id}` with explanations and history; Himalayas attribution (ADR 0011)
- Matching loop: follows MLflow's `champion` (an audit row per change, last one kept while MLflow is down), scores new and changed jobs in batches of 50 with the champion, keeps results per model version (instant rollback), circuit breaker, `/api/v1/models` and `/api/v1/models/activations`, matching metrics; compose `matcher-train` service for train/promote/models
- `/metrics` (Prometheus): API requests and latency by route pattern, sync runs by kind and status, postings by result; logs use `ts` like the matcher's
- Application tracking: statuses saved, applied, interview, offer, rejected, archived (none = discovered), an event log with backdating and notes, corrections with `force`, `/api/v1/applications` and `/api/v1/labels/export` (JSONL labels for retraining)
- Matcher client: batches of up to 50, retries on 503 and connection errors, request IDs forwarded, startup version check against the pinned release, `/readyz` reports the matcher (`ok`, `unreachable`, `incompatible`, `disabled`) without failing; contract tests against the pinned OpenAPI with recorded real responses; matcher pinned to 0.2.1; compose: matcher cache volume
- Candidate profiles: JSON Schema v1, immutable revisions, `jobradar profile import <file.yaml>` (idempotent), `/api/v1/profiles`, a fictional example profile
- Himalayas adapter: saved searches with validated filters, 500-job cap reported as a partial run, expired jobs closed on every sync, Himalayas terms recorded
- Source config is validated when a source is created (Lever region, Himalayas filters)
- Ashby adapter: salary from the compensation summary, workplace and employment type, all locations; unlisted postings skipped
- Lever adapter: paged fetch, EU sites via `{"region":"eu"}`, salary, workplace type, country, posting date; requirement lists kept in the description
- Project spec and Phase 0 architecture proposal for review
- ADRs 0001–0009 (Proposed): repos, Go stack, compose, matcher contract, ingestion, profiles, model versions, applications, observability
- ADR 0010 (Proposed): model lifecycle in MLflow, replacing the rejected 0007
- Git-ignore real profiles, exported labels and compose overrides
- Go backend skeleton: `jobradar serve` (`/healthz`, `/readyz` with schema check) and `migrate up|down|status`, env config, JSON logs with request IDs, problem+json errors, goose migrations, CI
- Local deployment: `compose.yaml` with Postgres 18, migrations, backend, matcher and MLflow 3.18, health checks, `jobradar healthcheck`, backup and restore in the README
- Job ingestion: sources API, scheduled and manual syncs with retries and rate limits, idempotent upserts, closing of vanished postings, cross-source dedup, Greenhouse adapter

### Changed
- Matcher pinned to 0.3.1 (1373865): skills rule from matcher ADR 0005, relevance-weighted scoring defaults, train/eval use the image's embedding model
- Matcher re-pinned to 822957e (0.2.1: `/metrics` needs the token, model versions carry their eval data); compose passes `MATCHER_REF` as the image's `GIT_COMMIT`, so each model version names the matcher commit
- Matcher client timeout 2 minutes per batch (ADR 0004 wording updated)
- ADR 0010: MLflow UI on host port 5001, psycopg 3, `--allowed-hosts` (as built in the compose PR)
- CI workflow disabled until the coding phases are done; checks run locally
- ADR 0001: the backend pins a matcher commit SHA instead of a tag; `scripts/pin-matcher.sh` and `contracts/`
- ADR 0010 accepted; ADR 0003 updated for the MLflow service and volumes
