# jobradar

Personal job intelligence platform: ingests remote job postings, matches them against a candidate profile, tracks applications (Go + Python, PostgreSQL).

## Getting the code

The system lives in two repos, cloned side by side ([ADR 0001](docs/adr/0001-two-repos-pinned-matcher.adr)):

```sh
git clone git@github.com:NobodyOne04/jobradar.git
git clone git@github.com:NobodyOne04/jobradar-matcher.git
cd jobradar
git -C ../jobradar-matcher switch --detach "$(cat contracts/MATCHER_REF)"   # the pinned matcher
```

To move the pin to a newer matcher commit (it must be on the matcher's `main`):

```sh
scripts/pin-matcher.sh <commit>   # updates contracts/: MATCHER_REF, MATCHER_VERSION, matcher.openapi.yaml
```
