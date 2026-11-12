# Changelog

## Unreleased

### Added
- Project spec and Phase 0 architecture proposal for review
- ADRs 0001–0009 (Proposed): repos, Go stack, compose, matcher contract, ingestion, profiles, model versions, applications, observability
- ADR 0010 (Proposed): model lifecycle in MLflow, replacing the rejected 0007
- Git-ignore real profiles, exported labels and compose overrides

### Changed
- ADR 0001: the backend pins a matcher commit SHA instead of a tag; `scripts/pin-matcher.sh` and `contracts/`
- ADR 0010 accepted; ADR 0003 updated for the MLflow service and volumes
