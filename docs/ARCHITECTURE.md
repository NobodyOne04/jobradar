# JobRadar: Phase 0 architecture proposal

**Status:** Proposed. Nothing in this document is approved yet. Once you approve a decision,
it becomes an ADR in `docs/adr/`. No code gets written before that (SPEC §14).
**Date:** 2026-11-13. Revision 2: your answers to Q1–Q5, two repos, model versioning (§5.1).
Revision 3: repo names confirmed; no registry, the matcher image is built locally (Q7).

Labels: **[F]** fact (from SPEC or a verifiable source), **[A]** assumption I need you to
confirm, **[R]** my recommendation.

Section order follows the SPEC §14 deliverables.

---

## 0. Requirements, constraints, assumptions

- [F] One user, a Senior Data Engineer looking for remote roles. Two services: Go owns state
  and workflows, Python owns matching. PostgreSQL, Docker Compose, GitHub Actions. No LLM APIs,
  no extra infrastructure, no frontend (SPEC §2, §3, §13).
- [A] Scale: a few hundred company boards and roughly 10k–50k open postings. A few syncs per
  day. That fits in one Postgres instance and one process per service with a lot of room to
  spare. Nothing below is designed for more.
- [F] Postings and the CV are in English for v1. Non-English postings are stored but not
  matched.
- [F] It runs locally on an always-on machine. The public API binds to localhost and has no
  auth.
- [F] The company/source list is decided later. v1 builds the adapters, and the list grows
  through `POST /sources`.
- [F] Model versions must support retraining and rolling back to a previous version (§5.1).
- [A] The candidate profile is hand-written from your CV as a YAML/JSON file (SPEC §5 allows a
  static profile). The system does not parse CVs.

## 1. Architecture proposal

```
                 ┌──────────────────────── docker compose ─────────────────────────┐
  you ── REST ──▶│  backend (Go)                         matcher (Python)          │
  (curl/http)    │  ├ api        ── HTTP /v1/match ───▶  ├ api (FastAPI)           │
                 │  ├ scheduler                          ├ extract (skills, reqs)  │
 Greenhouse ◀────│  ├ ingest/<source> adapters           ├ score (dimensions)      │
 Lever, Ashby,   │  ├ matching (client + orchestration)  └ model files baked in    │
 Himalayas       │  ├ profiles, applications                  (stateless, no DB)   │
                 │  └ store (pgx) ──────▶ postgres (volume)                        │
                 │  migrate (one-shot, same Go binary)                             │
                 └─────────────────────────────────────────────────────────────────┘
```

- [R] **The matcher is stateless.** Go sends the normalized job and the profile revision.
  Python returns a structured result, and Go stores it. Python has no database connection.
  Go stays the only owner of state (SPEC §2.1). Python can be scaled, replaced or rolled
  back on its own. Reproducibility then depends only on the inputs and the model version.
- [R] **Synchronous HTTP between the services, no queue.** Go runs matching in batches from
  its own scheduler. The work list comes from a query: "jobs with no result for the current
  (profile revision, active model version, job content hash)". No queue table or broker is needed,
  and the same query also handles retries and backfills.
- [R] **The scheduler runs inside the Go process:** tickers, plus a Postgres advisory lock so
  two backend instances never sync the same source at once. No cron container, no
  Redis/River/Temporal.

## 2. Go/Python service boundaries

| Concern | Go backend | Python matcher |
|---|---|---|
| Fetching from sources, retries, rate limits | ✔ | |
| Normalization (fields the source provides as structured data: title, company, location, salary fields, employment type) | ✔ | |
| Deduplication | ✔ | |
| Free-text understanding (skills, requirements vs nice-to-haves, seniority and years from text, remote/timezone hints in prose) | | ✔ |
| Scoring, explanations, confidence | | ✔ |
| Persisting match results, deciding what to (re)match | ✔ | |
| Profiles, applications, public REST API | ✔ | |

Rule [R]: **Go never interprets free text, and Python never decides what to store.** A new
source changes only `backend/internal/ingest/<source>`. The matcher only sees the normalized
job (SPEC §4: a new source must not require changes to the matching engine).

## 3. Repository structure (two repos)

[F] Your call: two repos, each validated on its own.

```
jobradar/                        (this repo) Go backend + the system as a whole
├─ cmd/jobradar/main.go          subcommands: serve, migrate
├─ internal/
│  ├─ api/                       HTTP handlers, request validation, error format
│  ├─ ingest/                    sync loop, the Adapter interface, dedup
│  │  ├─ greenhouse/  lever/  ashby/  himalayas/
│  ├─ matching/                  matcher client, batch orchestration, model activation
│  ├─ profiles/  applications/
│  ├─ store/                     SQL queries (sqlc output) and transactions
│  └─ config/                    env parsing and validation
├─ migrations/                   SQL files, embedded in the binary
├─ contracts/                    pinned matcher release: MATCHER_VERSION + matcher.openapi.yaml
├─ profiles/example.yaml         sample profile; your real one is git-ignored
├─ docs/  SPEC.md  ARCHITECTURE.md  adr/  api/backend.openapi.yaml
├─ compose.yaml  .env.example    the whole system; matcher built from ../jobradar-matcher
└─ .github/workflows/ci.yml

jobradar-matcher/                Python matching service
├─ src/matcher/{api,extract,score,taxonomy,train,registry}/
├─ data/skills.yaml              curated skill taxonomy (aliases, categories, relations)
├─ eval/                         labeled dataset and evaluation script
├─ openapi.yaml                  the contract, generated by FastAPI and committed
├─ docs/adr/                     matcher-internal decisions (ML, model format)
├─ Dockerfile  tests/
└─ .github/workflows/ci.yml      lint, test, eval gate, image build check
```

How the split stays safe [R]:
- **The matcher owns its contract.** `openapi.yaml` lives in the matcher repo and is
  versioned with it. A matcher CI test fails if the generated spec differs from the
  committed one.
- **The backend pins a matcher release, with no registry** (Q7: local only).
  `contracts/MATCHER_VERSION` holds a matcher git tag (`v1.4.0`), and
  `contracts/matcher.openapi.yaml` is the contract copied from that tag. Backend contract
  tests check the Go client against that copy. Upgrading the matcher is one backend PR that
  bumps both together.
- **Building it:** the matcher repo is checked out next to this one at the pinned tag.
  `compose.yaml` builds it from `${MATCHER_SRC:-../jobradar-matcher}` and tags the image
  `jobradar-matcher:${MATCHER_VERSION}`. Docker keeps the earlier tags locally, so rolling
  back a release means starting the previous tag again, with no rebuild while that image
  still exists.
- **Startup check:** the backend calls `/v1/version` and refuses to start matching if the
  major version differs from the pinned one, or logs a warning on a minor or patch
  difference. This catches a sibling checkout on the wrong tag.
- **Breaking contract changes go to a new path** (`/v2`). The matcher serves both until the
  backend has moved over.
- **System-level docs** (SPEC, this document, cross-service ADRs) live in `jobradar`.
  Matcher-internal ADRs live in `jobradar-matcher`.

## 4. Database schema proposal

```sql
sources            (id, kind text CHECK IN ('greenhouse','lever','ashby','himalayas'),
                    board_token text, display_name, enabled bool, config jsonb,
                    created_at, UNIQUE (kind, board_token))
sync_runs          (id, source_id → sources, started_at, finished_at,
                    status ('running','succeeded','failed','partial'),
                    fetched, created, updated, unchanged, closed int, error text)

jobs               (id, title, company_name, company_key,        -- canonical job
                    description_text, description_html,
                    location_text, remote_policy ('remote','hybrid','onsite','unknown'),
                    countries text[], employment_type ('full_time','contract',…,'unknown'),
                    salary_min, salary_max numeric, salary_currency char(3), salary_period,
                    posted_at, closed_at, content_hash, dedup_key,
                    created_at, updated_at)
source_postings    (id, source_id → sources, external_id, job_id → jobs,
                    url, raw_payload jsonb, payload_hash,
                    first_seen_at, last_seen_at, closed_at,
                    UNIQUE (source_id, external_id))

candidate_profiles (id, name, created_at)
profile_revisions  (id, profile_id → candidate_profiles, revision int, schema_version int,
                    data jsonb, created_at, UNIQUE (profile_id, revision))   -- immutable

model_versions     (id, version text UNIQUE, matcher_version text, manifest jsonb,
                    metrics jsonb, status ('candidate','active','retired','rejected'),
                    activated_at, created_at)   -- at most one 'active' (partial unique index)
model_activations  (id, model_version_id → model_versions, previous_id → model_versions,
                    reason text, activated_at)  -- audit trail: every activation and rollback
match_results      (id, job_id → jobs, profile_revision_id → profile_revisions,
                    model_version_id → model_versions, job_content_hash,
                    score smallint, band text, confidence text, completeness real,
                    hard_constraints text ('pass','fail','unknown'),
                    details jsonb,          -- dimensions, matched/missing/transferable skills
                    latency_ms int, created_at,
                    UNIQUE (job_id, profile_revision_id, model_version_id, job_content_hash))

applications       (id, job_id → jobs, profile_id → candidate_profiles, status, notes,
                    created_at, updated_at, UNIQUE (job_id, profile_id))
application_events (id, application_id → applications, from_status, to_status, note,
                    occurred_at)
```

Choices to review:
- [R] **Raw payloads live in `source_postings`, and canonical jobs in `jobs`.** Many postings
  can point to one job, which is how cross-source dedup works. Only the latest raw payload
  is kept, with `payload_hash` to skip unchanged ones. Keeping every version of a raw
  payload is left out until you need history.
- [R] **The profile revision is one immutable `jsonb` document** with a `schema_version`,
  validated by the API, not split into a dozen tables. Matching always reads the whole
  document, and revisions are compared as whole documents. No query today needs "all
  profiles with skill X". Trade-off: Postgres does not enforce the profile's internal
  structure, so validation lives in code and in the JSON Schema.
- [R] **The skill lists in `match_results.details` are `jsonb`.** Same reasoning: they are
  the matcher's output, read back whole.
- [R] **No `companies` table yet.** `company_key` (normalized name) is enough for dedup and
  filtering. A table can come later if company-level data shows up.
- [R] **Application status is not a column on `jobs`.** "Discovered" means a job with no
  `applications` row. The API returns `discovered` for it, so the spec's status list stays
  intact. Every transition is written to `application_events`, which supplies the
  timestamps (SPEC §7).
- [R] Matching results are not stored redundantly. `jobs` gets no "latest score" column, and
  the API joins on the current revision and scoring version instead. If that ever gets slow,
  an index is the fix.

## 5. Matching architecture: investigation and comparison

The problem [F]: compare one structured profile with a semi-structured job (structured
fields plus a free-text description). The output must be explainable, reproducible, honest
about missing data, and testable (SPEC §6). [A] On day one there are **no labeled data**:
you have not yet rated jobs as good or bad matches.

| Approach | Quality | Explainable | Reproducible | Latency / resources | Needs labels | Verdict |
|---|---|---|---|---|---|---|
| A. Rules + skill taxonomy (keyword/phrase matching, hard constraints) | good on skills and constraints, misses paraphrase | fully | fully | µs, tiny | no | **core** |
| B. TF-IDF / BM25 similarity, profile vs job | weak: rewards shared words, not fit | poorly ("shared terms") | yes | tiny | no | no: A explains the same signal better |
| C. Classical supervised ML (logistic regression / GBM on features) | best once labeled | yes, with linear models | yes | tiny | **yes, hundreds** | **later**: train on your own saved/applied/rejected labels to tune weights |
| D. Sentence embeddings (small bi-encoder, e.g. bge-small / MiniLM, ~22–33M params, CPU) | catches paraphrase: "built batch pipelines" ≈ "ETL development" | partly (which sentences matched) | yes, with a pinned model and CPU inference | ~5–20 ms per job on CPU [A, to measure] | no | **one dimension**, not the whole score |
| E. Cross-encoder / fine-tuned transformer | potentially best | poor | yes | 10–100× D, GPU-friendly | yes | no: no data to fine-tune, cost too high |
| F. LLM extraction / judging | good extraction | plausible but not checkable | no (sampling, model drift) | API cost or a heavy local model | no | excluded by SPEC §13 |

### [R] Recommendation: hybrid, deterministic core + one embedding dimension (A + D, then C)

1. **Extraction (deterministic).** A curated taxonomy, `data/skills.yaml`, has a few hundred
   data/backend skills, each with aliases (`k8s`→Kubernetes, `GCP`/`Google Cloud`),
   a category (orchestration, warehouse, streaming…) and `related` edges (Airflow ↔ Dagster ↔
   Prefect). A phrase matcher (spaCy `PhraseMatcher` or plain token matching) finds skills in
   the posting. Section headings ("Requirements", "Nice to have", "Bonus") tag each skill as
   required or preferred. Regexes pick up years of experience, seniority words in the title,
   salary text and timezone/region phrases that the structured fields lack.
2. **Dimensions.** Each dimension returns `{score ∈ [0,1] or null, weight, evidence[]}`:
   - skills: weighted coverage of the required, then the preferred skills. A `related` skill
     counts as **transferable** and gets partial credit.
   - seniority/experience: title level and years against the profile.
   - responsibilities: embedding similarity between the posting's responsibility sentences
     and your experience bullets (D). The evidence is the best-matching sentence pairs.
   - compensation, location/timezone, employment type: compared with your preferences.
3. **Hard constraints are gates** with three outcomes, `pass`, `fail` or `unknown`
   (for example: not remote, a country you cannot work from, salary max below your floor).
   `fail` puts the job in the `excluded` band whatever its score. `unknown` never fails a
   job (SPEC §6.3). It lowers confidence instead.
4. **Score, confidence and completeness are separate** (SPEC §6.4):
   - score = the weighted mean over the **known** dimensions only, rounded to a multiple
     of 5 and shown with a band (`strong` / `good` / `weak` / `excluded`). There is no
     `87.34`, which would be misleading precision.
   - completeness = the share of the total weight whose dimensions had data.
   - confidence = `high` / `medium` / `low`, from completeness and extraction quality
     (for example, a posting with zero recognized skills).
5. **Explanations** come straight from the evidence: matched, missing and transferable
   skills, the constraint results with the reason for each, the top responsibility pairs,
   and the unrecognized terms (which feed growth of the taxonomy).
6. **Traceability:** every result records the model version that produced it, and that
   version's manifest pins everything else (§5.1, SPEC §6.7).
7. **Later (C):** once you have about 150+ jobs marked saved/applied/rejected, fit a logistic
   regression on the dimension scores. That replaces the hand-set weights with learned
   ones, and stays explainable. That is a retrain that produces a new model version (§5.1),
   not a new architecture.

Why not something else: embeddings alone cannot apply hard constraints or say *which* skill
is missing. Supervised ML has no labels yet. Cross-encoders and LLMs cost a lot and give up
the explainability and reproducibility the spec asks for. Rules alone miss paraphrase in
responsibilities, the one place where a small embedding model clearly adds something.

### 5.1 Model versioning, retraining and rollback [R]

What counts as "the model": everything that decides a score. That is the taxonomy,
weights, thresholds and band cut-offs, and later the trained coefficients, plus the
pretrained embedding model. These change at different speeds, so there are two version
axes:

| Axis | What it covers | Changes when | Rollback |
|---|---|---|---|
| **Matcher release** (`v1.4.0`, image tag) | code, the pretrained embedding model files (baked in the image, pinned revision) | code or embedding model changes | start the previous `jobradar-matcher:vX.Y.Z` image (kept locally) |
| **Model version** (`m-0007`) | an immutable **bundle**: `manifest.json`, `taxonomy.yaml`, `weights.json` (hand-set or trained), `thresholds.json`, `metrics.json`, plus the snapshot of training labels it came from | taxonomy edits, re-tuning, retraining | activate the previous model version: one API call, no rebuild |

The bundle manifest pins: version id, parent version, creation time, matcher release
range it works with, embedding model id and revision, training data snapshot hash, random
seed, and the eval metrics. Every bundle is a few hundred KB, because the large embedding
model is not part of it.

**Where bundles live.** In a Docker volume, `models/<version>/`, mounted read-only into the
matcher and written only by the training command. A bundle is never modified after it is
written. The matcher loads every bundle that passes a checksum and compatibility check, and
lists them at `GET /v1/models`.

**Who decides which version is active: the backend**, since it owns state. Activation is a
row in `model_versions` plus an audit row in `model_activations`. Every match request
names the model version explicitly, so the matcher never holds a hidden "current" setting.

**Retrain flow:**
1. `GET /api/v1/labels/export`: your saved/applied/rejected decisions and eval grades,
   as a snapshot file.
2. `docker compose run matcher train --labels snapshot.jsonl --parent m-0006` writes
   bundle `m-0007`, then runs the eval set on it and on the active version.
3. The backend registers it as `candidate`, and `GET /api/v1/models/m-0007` shows its
   metrics next to the active version's.
4. `POST /api/v1/models/m-0007/activate`. This is refused if it fails the eval gate
   (for example, NDCG@10 below the parent's minus tolerance, or any false `fail`), unless
   you pass `force` with a reason.
5. The backend re-matches open jobs with the new version in the background. Until a job's
   new result is ready, the API shows the result from the previous version, marked as such.

**Rollback:** `POST /api/v1/models/m-0006/activate` with a reason. Results from m-0006 are
still in `match_results` (the unique key includes the model version), so the rollback takes
effect at once, without recomputing anything. Results from the bad version are kept and
tagged, so the bug can be investigated.

**Backup:** the bundles volume is copied with the Postgres backup. Each bundle holds its
own training snapshot and seed, so it can also be rebuilt from scratch.

Alternatives considered:
- *Bake bundles into the image* (version = image tag): simplest, but every retrain becomes
  a release and an image build, and rolling back the model also rolls back code. Rejected
  because you asked for a quick retrain and rollback loop.
- *MLflow model registry*: adds a server and storage for a few-KB artifact. Rejected per
  SPEC §13. It becomes worth reconsidering only with many models or many people.
- *Bundles in Postgres (bytea)*: possible, but the matcher would then need DB access or
  receive bundles over HTTP, which blurs the boundary. A volume is simpler.

### Library and model options [R, to confirm with a spike in Phase 3]
- Embeddings: **fastembed** (ONNX Runtime, CPU, image ~300–500 MB) rather than
  **sentence-transformers** (PyTorch, image well over 1 GB). Both serve
  `BAAI/bge-small-en-v1.5` and `all-MiniLM-L6-v2`. The model files are downloaded at image
  build time with a pinned revision, and nothing is fetched at runtime.
- The candidate model is picked on the evaluation set below, not on reputation.
- Taxonomy seed: hand-curated YAML. ESCO / O*NET are too broad and too weak on modern data
  tooling to adopt wholesale, though useful as a reference.

## 6. Evaluation strategy

- **Labeled set**, versioned in `matcher/eval/`: about 150 real postings, which you grade
  0–3 (no / weak / good / strong fit for you), plus about 50 with hand-annotated skills.
  Postings are stored as normalized jobs. A fixed profile fixture sits next to them.
- **Metrics:**
  - extraction: skill precision and recall against the annotations.
  - ranking: NDCG@10 and precision@10 against your grades.
  - constraints: zero false `fail` on the labeled set. Wrongly excluding a good job is
    the costliest error.
- **In CI:** the evaluation runs on every matcher PR. It fails if a metric drops more than
  a set tolerance below the committed baseline, and a new baseline is committed on purpose
  in the PR. Unit tests cover each dimension, plus golden tests (fixed input → fixed
  output) for reproducibility.
- **Online signal (later):** your application actions become labels for step C.

## 7. API contracts

### 7.1 Matcher (internal), `/v1`, OpenAPI generated by FastAPI and committed

```
POST /v1/match      {model_version, profile: ProfileRevision, jobs: [NormalizedJob]}  (max N per batch)
                 →  {model_version, matcher_version, results: [{job_id, score, band, confidence,
                     completeness, hard_constraints, dimensions[], skills:{matched, missing,
                     transferable, unrecognized}, explanations[]}]}
GET  /v1/models  → bundles loaded: [{version, manifest, metrics}]     GET /v1/version → matcher release
                    (unknown or incompatible model_version → 422 problem+json)
GET  /healthz    → process alive        GET /readyz → model loaded
```
- Auth: `Authorization: Bearer $MATCHER_TOKEN`, a shared secret from env [R]. The port is
  published only on the compose network.
- Errors: RFC 9457 `application/problem+json`. Validation errors carry the field paths.
- Contract test: Go tests decode recorded matcher responses against the committed OpenAPI
  file, and a Python test checks that the generated OpenAPI matches the committed one.

### 7.2 Backend (public), `/api/v1`, OpenAPI written by hand, problem+json errors

```
GET    /jobs?q=&band=&min_score=&remote=&source=&status=&posted_after=&cursor=&limit=
GET    /jobs/{id}                       canonical job, its postings, current match result
GET    /sources   POST /sources   PATCH /sources/{id}   POST /sources/{id}/sync
GET    /sources/{id}/runs
GET    /profiles  POST /profiles  POST /profiles/{id}/revisions  GET /profiles/{id}/revisions/{rev}
GET    /matches?profile_id=&band=      POST /matches/recompute   (async, returns a run id)
GET    /models   GET /models/{v}   POST /models/{v}/activate {reason, force?}   GET /models/activations
GET    /labels/export
GET    /applications   PUT /applications/{job_id}  {status, note}
GET    /applications/{job_id}/events
GET    /healthz   GET /readyz (DB reachable, migrations at the latest version; matcher status reported, not required)
```
- Cursor pagination on lists. Every request gets a request ID: the backend reads
  `X-Request-ID` or generates one, and passes it on to the matcher and into every log line.

## 8. Implementation roadmap (each phase is one or more ADR-sized PRs)

| Phase | Delivers | Acceptance criteria |
|---|---|---|
| 1 Foundation | both repos, Go `serve`/`migrate`, Python FastAPI skeleton, initial migration, compose with health checks, CI (lint, test, govulncheck, pip-audit) | `cp .env.example .env && docker compose up` brings all three up healthy. `/readyz` is green on both services. CI is green. |
| 2 Ingestion | Greenhouse + Lever adapters first, then Ashby + Himalayas; normalization, dedup, scheduler, sync runs | Syncing the same board twice gives 0 new rows (idempotent). Recorded HTTP fixtures cover each adapter. 429/5xx responses are retried with backoff. A closed posting gets `closed_at` set. A cross-source duplicate links to one job. |
| 3 Matching | profile schema + revisions, taxonomy, extraction, dimensions, scoring, explanations, eval set, Go orchestration, model bundles + activation/rollback | The eval script reports metrics, and a baseline is committed. Activating m-0001, then m-0002, then rolling back to m-0001 serves m-0001 results again with no recompute. Same inputs give a byte-identical result. A missing salary gives `unknown`, not `fail`. Results are stored with their versions. |
| 4 Applications & API | job search/filter, match listing, application lifecycle and events | Every endpoint is in the OpenAPI file and covered by contract tests. Every status transition is recorded with a timestamp. |
| 5 Hardening | metrics endpoint, timeouts/retry review, security audit, load test of match batches, docs | No open high-severity security findings. A matcher outage degrades gracefully: ingestion keeps running, results are "pending". The README alone gets a fresh clone running. |

The MVP (useful end to end) is phases 1–3 plus a minimal `GET /jobs` sorted by score.

## 9. Key decisions needing your approval (future ADRs)

| # | Decision | Recommendation | Main alternative |
|---|---|---|---|
| 1 | Repo shape | **two repos** (your call): `jobradar` (Go + compose + system docs), `jobradar-matcher`; contract owned by the matcher, pinned by tag in the backend | monorepo (rejected by you) |
| 2 | Go stack | stdlib `net/http` routing, **pgx v5** + **sqlc**, **goose** migrations embedded, `log/slog` | chi/echo; GORM (rejected: hides SQL); golang-migrate |
| 3 | Python stack | **FastAPI** + Pydantic, **uv**, pytest, ruff, mypy | Flask/Litestar; Poetry |
| 4 | Service interaction | stateless matcher, sync HTTP batches, bearer token | matcher reads the DB itself; a queue |
| 5 | Scheduling | in-process scheduler + advisory locks; work found by query | separate cron/worker container; River (Postgres job queue) |
| 6 | Schema | as in §4 (jsonb profile revisions, postings → jobs) | fully relational profile |
| 7 | Matching | hybrid A+D, banded score, separate confidence, C later | embeddings-only; supervised from day one |
| 7a | Model versioning | two axes (matcher release, model bundle); immutable bundles on a volume; activation and rollback in the backend, gated by eval (§5.1) | bundles in the image; MLflow |
| 8 | Evaluation | labeled set graded by you + CI regression gate | unit tests only |
| 9 | Observability | slog JSON logs + request IDs; Prometheus-format `/metrics` on both, **no** Prometheus/Grafana containers until needed | OpenTelemetry tracing stack |
| 10 | Versions | Go and Python pinned to the current stable release at Phase 1 start (Python capped at the latest version the ONNX Runtime wheels support) | n/a |

## 10. Technical risks

1. **Where the companies come from** (high). Greenhouse, Lever and Ashby expose public
   *per-company* board endpoints [F: verify the exact endpoints and terms in Phase 2]. Someone
   has to supply the list of company board tokens. Himalayas is the only aggregator in the
   list. Without a seed list, coverage will be thin (see Q3).
2. **Terms of use and rate limits** (medium). Public endpoints still have terms (Himalayas
   asks for attribution, for example). Each adapter's terms are checked before it is built.
   Polling is polite: few syncs, backoff, honest User-Agent.
3. **Extraction quality on free-text postings** (high, core to value). The taxonomy needs
   upkeep. Mitigation: report unrecognized terms, eval metrics in CI, grow the taxonomy
   from real postings.
4. **Score calibration** (medium). Embedding cosine values are not probabilities, so the
   thresholds are tuned on the eval set and the score is shown in bands.
5. **Cross-source dedup** (medium). The deterministic key (company + title + location)
   misses variants. Start strict: a missed duplicate costs less than a wrong merge.
   Fuzzy matching comes later, only if duplicates prove common.
6. **Eval set effort** (medium). It needs a few hours of your grading. Without it, the
   matching quality claims cannot be checked.
7. **Personal data** (medium). CV-derived data lives in the DB and the profile file:
   the real profile is git-ignored, never logged, and stays on your machine (see Q2).
8. **Incremental sync** (low). These APIs mostly return a board's full current list, not
   changes since a time [A]. "Incremental" therefore means a full fetch per board plus a
   diff by `payload_hash`, which is cheap at this scale.

9. **Cross-repo drift** (medium, new with two repos). The backend could talk to a matcher
   version it was not tested against. Mitigation: a pinned version and contract copy,
   tested in backend CI, and `/v1/version` is checked at backend startup,
   which refuses an incompatible major version.
10. **No end-to-end test in CI** (low, from Q7). Each repo's CI tests its own side of the
    contract. Running both services together happens only locally (`docker compose up` plus
    a smoke script), until the matcher is published somewhere CI can pull it from.
11. **Bundle and release compatibility** (low). An old bundle may not load on a new matcher.
    The manifest declares a compatible release range, and the matcher refuses bundles
    outside it, so they cannot fail silently.

## 11. Open questions (answers may change the design)

All answered:
Q1 local, always on · Q2 localhost, no auth · Q3 later, sources are extensible ·
Q4 yes, you'll grade the eval set · Q5 English only for v1 · Q6 `jobradar` +
`jobradar-matcher` · Q7 no registry, the matcher is built locally from a sibling checkout.
