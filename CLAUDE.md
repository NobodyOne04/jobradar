# jobradar

Personal job intelligence platform: a Go core service (ingestion, tracking, REST API) and a
Python matching service, on PostgreSQL, run with Docker Compose.

## Commands
```sh
scripts/pin-matcher.sh <commit>   # pin a matcher commit on its main; updates contracts/
git -C ../jobradar-matcher switch --detach "$(cat contracts/MATCHER_REF)"
```

## Layout
- `docs/SPEC.md`: the project specification
- `docs/ARCHITECTURE.md`: Phase 0 proposal (status Proposed until I approve; approved
  decisions move to `docs/adr/`)
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
- Candidate data is personal: never hardcode it in the matching engine, never log it.
