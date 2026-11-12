# jobradar

Personal job intelligence platform: a Go core service (ingestion, tracking, REST API) and a
Python matching service, on PostgreSQL, run with Docker Compose.

## Commands
None yet: Phase 0 (architecture) comes before any code.

## Layout
- `docs/SPEC.md`: the project specification (to be added)

## Conventions
- Phase 0 first: architecture proposal and ADRs, approved by me, before any implementation
  code (SPEC §14). I own every significant architectural decision.
- One ADR, one PR: the ADR and its implementation; lower numbers first.
- No LLM API dependency, no extra infrastructure, no frontend without a stated need (SPEC §13).
- No committed secrets: configuration comes from the environment; `.env*` is ignored.
- Candidate data is personal: never hardcode it in the matching engine, never log it.
