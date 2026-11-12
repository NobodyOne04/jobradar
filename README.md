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

## Run it

Needs Docker with Compose v2, and both repos cloned side by side, as above.

```sh
cp .env.example .env        # then replace every change-me (openssl rand -hex 24)
docker compose up -d --build
curl localhost:8080/readyz  # {"status":"ready"}
```

| What | Where |
|---|---|
| API | http://localhost:8080 |
| MLflow UI | http://localhost:5001 (5000 is taken by macOS AirPlay Receiver) |
| Postgres, matcher | not published; inside the compose network only |

`docker compose ps` shows health. Containers restart on their own after a reboot or a crash.
The database passwords in `.env` are applied only when the `pgdata` volume is first created.

## Add job sources

Sources are rows, not code: add a company's board through the API, and the scheduler
syncs it every `SYNC_INTERVAL` (6h). Supported so far:

| kind | board_token | config |
|---|---|---|
| `greenhouse` | the `{board}` in `boards.greenhouse.io/{board}` | none |
| `lever` | the `{site}` in `jobs.lever.co/{site}` | `{"region":"eu"}` for `jobs.eu.lever.co` sites |

```sh
curl -X POST localhost:8080/api/v1/sources \
  -d '{"kind":"greenhouse","board_token":"gitlab","display_name":"GitLab"}'
curl -X POST localhost:8080/api/v1/sources/1/sync        # sync now instead of waiting
curl localhost:8080/api/v1/sources/1/runs                # outcome: fetched, created, closed, ...
curl -X PATCH localhost:8080/api/v1/sources/1 -d '{"enabled":false}'
```

## Back up and restore

Everything that matters is in two volumes: `pgdata` (the `jobradar` and `mlflow` databases)
and `mlartifacts` (model files).

```sh
mkdir -p backup
docker compose exec -T postgres pg_dump -U postgres -Fc jobradar > backup/jobradar.dump
docker compose exec -T postgres pg_dump -U postgres -Fc mlflow   > backup/mlflow.dump
docker run --rm -v jobradar_mlartifacts:/a:ro -v "$PWD/backup":/b alpine tar czf /b/mlartifacts.tgz -C /a .
```

Restore into a fresh stack (`docker compose up -d`, then):

```sh
docker compose stop backend mlflow
docker compose exec -T postgres pg_restore -U postgres -d jobradar --clean --if-exists --no-owner --role=jobradar < backup/jobradar.dump
docker compose exec -T postgres pg_restore -U postgres -d mlflow   --clean --if-exists --no-owner --role=mlflow   < backup/mlflow.dump
docker run --rm -v jobradar_mlartifacts:/a -v "$PWD/backup":/b alpine tar xzf /b/mlartifacts.tgz -C /a
docker compose start backend mlflow
```

`backup/` holds personal data (your profile, applications, labels): keep it off git and
off shared drives.
