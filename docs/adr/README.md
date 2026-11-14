# Architecture Decision Records

Files are `NNNN-title.adr` (Markdown). Lower numbers are implemented first, one PR each
(the ADR flips to Accepted in the PR that implements it, after your approval).
Matcher-internal decisions live in `jobradar-matcher/docs/adr/`.

| # | Decision | Status |
|---|---|---|
| [0001](0001-two-repos-pinned-matcher.adr) | Two repositories; the backend pins a matcher release | Accepted |
| [0002](0002-go-backend-stack.adr) | Go backend stack: stdlib HTTP, pgx + sqlc, goose, slog | Accepted |
| [0003](0003-local-deployment-compose.adr) | Local deployment: one Docker Compose file, always-on, localhost only | Accepted |
| [0004](0004-matcher-service-contract.adr) | Backend ↔ matcher: a stateless matcher over synchronous HTTP batches | Accepted |
| [0005](0005-job-ingestion.adr) | Job ingestion: source adapters, in-process scheduler, idempotent upserts | Accepted |
| [0006](0006-candidate-profiles.adr) | Candidate profiles: immutable, schema-validated JSON revisions | Accepted |
| [0007](0007-matching-orchestration-model-versions.adr) | Matching orchestration, model activation and rollback | Rejected (MLflow instead, see 0010) |
| [0008](0008-application-tracking.adr) | Application tracking: a status per job and profile, with an event log | Accepted |
| [0009](0009-observability.adr) | Observability: JSON logs with request IDs, Prometheus-format metrics | Accepted |
| [0010](0010-matching-mlflow-model-lifecycle.adr) | Matching orchestration with MLflow: the `champion` alias decides the live model | Accepted |
| [0011](0011-job-search-api.adr) | Job search API: jobs with their match and application status, keyset pages | Accepted |
| [0012](0012-web-ui.adr) | Web UI: static HTML+JS in its own repo, served by nginx on the same origin | Accepted |
| [0013](0013-feed-sources.adr) | Feed sources: Jobicy, Remote OK, Remotive, Dev Global Jobs; closed after 30 days unseen | Accepted |
