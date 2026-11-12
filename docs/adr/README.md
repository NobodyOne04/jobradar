# Architecture Decision Records

Files are `NNNN-title.adr` (Markdown). Lower numbers are implemented first, one PR each
(the ADR flips to Accepted in the PR that implements it, after your approval).
Matcher-internal decisions live in `jobradar-matcher/docs/adr/`.

| # | Decision | Status |
|---|---|---|
| [0001](0001-two-repos-pinned-matcher.adr) | Two repositories; the backend pins a matcher release | Accepted |
| [0002](0002-go-backend-stack.adr) | Go backend stack: stdlib HTTP, pgx + sqlc, goose, slog | Proposed |
| [0003](0003-local-deployment-compose.adr) | Local deployment: one Docker Compose file, always-on, localhost only | Proposed |
| [0004](0004-matcher-service-contract.adr) | Backend ↔ matcher: a stateless matcher over synchronous HTTP batches | Proposed |
| [0005](0005-job-ingestion.adr) | Job ingestion: source adapters, in-process scheduler, idempotent upserts | Proposed |
| [0006](0006-candidate-profiles.adr) | Candidate profiles: immutable, schema-validated JSON revisions | Proposed |
| [0007](0007-matching-orchestration-model-versions.adr) | Matching orchestration, model activation and rollback | Proposed |
| [0008](0008-application-tracking.adr) | Application tracking: a status per job and profile, with an event log | Proposed |
| [0009](0009-observability.adr) | Observability: JSON logs with request IDs, Prometheus-format metrics | Proposed |
