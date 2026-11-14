# jobradar

Personal job intelligence platform: ingests remote job postings, matches them against a candidate profile, tracks applications (Go + Python, PostgreSQL).

## Getting the code

The system lives in three repos, cloned side by side ([ADR 0001](docs/adr/0001-two-repos-pinned-matcher.adr)):

```sh
git clone git@github.com:NobodyOne04/jobradar.git
git clone git@github.com:NobodyOne04/jobradar-matcher.git
git clone git@github.com:NobodyOne04/jobradar-web.git      # the web UI (ADR 0012)
cd jobradar
git -C ../jobradar-matcher switch --detach "$(cat contracts/MATCHER_REF)"   # the pinned matcher
```

To move the pin to a newer matcher commit (it must be on the matcher's `main`):

```sh
scripts/pin-matcher.sh <commit>   # updates contracts/: MATCHER_REF, MATCHER_VERSION, matcher.openapi.yaml
```

## Run it

Needs Docker with Compose v2, and the three repos cloned side by side, as above.

```sh
cp .env.example .env        # then replace every change-me (openssl rand -hex 24)
docker compose up -d --build
curl localhost:8080/readyz  # {"status":"ready"}
```

| What | Where |
|---|---|
| Web UI | http://localhost:3000 (jobs, matches, application status) |
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
| `ashby` | the `{board}` in `jobs.ashbyhq.com/{board}` (case matters) | none |
| `himalayas` | a search query, e.g. `data engineer` (a saved search over all remote jobs) | optional filters: `seniority`, `country`, `employment_type`, `timezone`, `company`, `sort` (strings), `worldwide`, `exclude_worldwide` (true/false) |
| `jobicy` | a title keyword, or `all` (a remote-jobs feed) | optional `geo`, `industry` (Jobicy slugs) |
| `remoteok` | a tag, e.g. `data`, or `all` | none |
| `remotive` | a search term, or `all` | optional `category`; synced at most every 6h (its terms) |
| `devglobaljobs` | a search term, e.g. `data engineer`, or `all` | optional `category`, `country` |
| `smartrecruiters` | the `{company}` in `jobs.smartrecruiters.com/{company}` (case matters) | none |
| `workable` | the `{account}` in `apply.workable.com/{account}` | none |
| `recruitee` | the `{company}` in `{company}.recruitee.com` | none |
| `personio` | the `{company}` in `{company}.jobs.personio.de` | `{"domain":"com"}` for `jobs.personio.com` boards |
| `breezy` | the `{company}` in `{company}.breezy.hr` | none (its jobs have no description) |
| `teamtailor` | the career site's host, e.g. `career.teamtailor.com` or `careers.example.com` | none |
| `rippling` | the `{board}` in `ats.rippling.com/{board}/jobs` | none |

A first sync that finds no jobs at a company says "check board_token" in its run (an unknown
SmartRecruiters company looks empty rather than failing). SmartRecruiters and Rippling need one
request per new or changed job, so a big board's first sync takes a while
([ADR 0014](docs/adr/0014-more-ats-sources.adr)).

Jobicy, Remote OK, Remotive and Dev Global Jobs are feeds of recent jobs ([ADR 0013](docs/adr/0013-feed-sources.adr)):
a job that drops out of the feed stays open until no sync has seen it for 30 days. Their jobs
show "via <board>" next to their link, as their terms require; Jobicy syncs at most hourly and
Remotive every 6h (a manual sync sooner gets 429).

A Himalayas search stops at 500 jobs; a broader one is stored up to that and its run is
marked `partial` ("narrow the search"). Himalayas' terms apply to its jobs: apply through the
stored link (it is Himalayas' `applicationLink`), show "via Himalayas" wherever they are
displayed, and don't pass them on to other job boards or feeds.

```sh
curl -X POST localhost:8080/api/v1/sources \
  -d '{"kind":"greenhouse","board_token":"gitlab","display_name":"GitLab"}'
curl -X POST localhost:8080/api/v1/sources/1/sync        # sync now instead of waiting
curl localhost:8080/api/v1/sources/1/runs                # outcome: fetched, created, closed, ...
curl -X PATCH localhost:8080/api/v1/sources/1 -d '{"enabled":false}'
```

## Your profile

The matcher compares jobs with a candidate profile you write from your CV
([ADR 0006](docs/adr/0006-candidate-profiles.adr), schema in `docs/api/profile.schema.json`).

```sh
cp profiles/example.yaml profiles/anna.yaml      # git-ignored: only example.yaml is tracked
$EDITOR profiles/anna.yaml
docker compose run --rm -v "$PWD/profiles:/profiles:ro" migrate profile import /profiles/anna.yaml
```

The file name is the profile name. Each import that changes something adds a new revision
(old ones are kept, so past match results stay explainable); importing an unchanged file
does nothing. The API has the same: `POST /api/v1/profiles {"name":"anna"}`, then
`POST /api/v1/profiles/1/revisions` with the JSON document, `GET .../revisions/latest`.

## Matching

Jobs are scored against your profile by the matcher, with the model version that MLflow's
`champion` alias points to ([ADR 0010](docs/adr/0010-matching-mlflow-model-lifecycle.adr)).
On a fresh install there is none yet, and matching waits (`/readyz` says `"champion":"none"`):

```sh
docker compose run --rm matcher-train train --from-defaults        # registers version 1
docker compose run --rm matcher-train promote 1 --reason "first model"
```

Within a minute the backend picks it up and scores every open job, then each new or changed
one (`/readyz` shows the champion; `jobradar_matching_pending_jobs` in `/metrics` counts what
is left). Results are kept per model version, so a rollback is instant:

```sh
docker compose run --rm matcher-train models                       # * marks the champion
docker compose run --rm matcher-train promote 1 --reason "v2 ranks remote jobs too low" --force
curl localhost:8080/api/v1/models/activations                      # every change, with reasons
```

The MLflow UI (http://localhost:5001) shows each version's runs and metrics; moving the alias
there works too, but skips the eval gate.

## Find jobs

Open jobs, best match first, with your application status
([ADR 0011](docs/adr/0011-job-search-api.adr)):

```sh
curl 'localhost:8080/api/v1/jobs'                                 # 25 per page, by score
curl 'localhost:8080/api/v1/jobs?q=data+engineer&remote=remote&min_score=60'
curl 'localhost:8080/api/v1/jobs?band=strong&band=good&sort=posted'
curl 'localhost:8080/api/v1/jobs?status=discovered&source=lever'
curl 'localhost:8080/api/v1/jobs?cursor=<next_cursor from the previous page>'
curl localhost:8080/api/v1/jobs/42        # description, why it matched, your history
```

Filters: `q` (full-text: title, company, description), `band` (repeatable), `min_score`,
`remote`, `source`, `status` (incl. `discovered`), `posted_after`, `sort` (score, posted,
updated), `limit` (1-100). Jobs that failed one of your hard constraints are hidden unless you
ask for `band=excluded`. A job not scored yet shows `match: null` (after the scored ones); a
result from an older model or profile shows `"stale": true` until it is re-scored. Jobs from
Himalayas carry `"attribution": "via Himalayas"`: keep it, and apply through their link.

## Track applications

Each job has a status for your profile ([ADR 0008](docs/adr/0008-application-tracking.adr)):
`saved → applied → interview → offer`, or `rejected` / `archived` from anywhere. A job you
have not touched is `discovered`. Every change is logged with its time and an optional note.

```sh
curl -X PUT localhost:8080/api/v1/applications/42 -d '{"status":"applied","note":"sent CV"}'
curl -X PUT localhost:8080/api/v1/applications/42 \
  -d '{"status":"interview","occurred_at":"2026-11-20T14:00:00Z"}'   # backdate a change
curl -X PUT localhost:8080/api/v1/applications/42 -d '{"status":"saved","force":true}'  # undo a step
curl localhost:8080/api/v1/applications?status=interview
curl localhost:8080/api/v1/applications/42/events
curl localhost:8080/api/v1/labels/export > labels.jsonl   # for retraining the matcher
```

Repeating a status without a note changes nothing. Moving backwards needs `"force": true`.
With several profiles, add `?profile_id=` (or `"profile_id"` in the body).

## Monitoring

`curl localhost:8080/metrics` (Prometheus text format, [ADR 0009](docs/adr/0009-observability.adr)):
requests by route and status code, request latency, sync runs by source kind and status,
postings by result (created, updated, unchanged, closed). Logs are JSON lines on stdout
(`docker compose logs backend`), each with `ts`, `level`, `msg` and a `request_id` that the
matcher's logs share.

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
