# JobRadar: Phase 0 architecture proposal

**Status:** Proposed. Nothing in this document is approved yet. Once you approve a decision,
it becomes an ADR in `docs/adr/`. No code gets written before that (SPEC §14).
**Date:** 2026-11-13

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
- [A] Postings and the CV are in English. Non-English postings are stored but not matched.
- [A] It runs on your laptop or one small always-on host. The public API is not exposed to the
  internet (see Q2).
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
  (profile revision, scoring version, job content hash)". No queue table or broker is needed,
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

## 3. Repository structure (monorepo)

```
jobradar/
├─ backend/                      Go module
│  ├─ cmd/jobradar/main.go       subcommands: serve, migrate
│  ├─ internal/
│  │  ├─ api/                    HTTP handlers, request validation, error format
│  │  ├─ ingest/                 sync loop, the Adapter interface, dedup
│  │  │  ├─ greenhouse/  lever/  ashby/  himalayas/
│  │  ├─ matching/               matcher client and batch orchestration
│  │  ├─ profiles/  applications/
│  │  ├─ store/                  SQL queries (sqlc output) and transactions
│  │  └─ config/                 env parsing and validation
│  └─ migrations/                SQL files, embedded in the binary
├─ matcher/                      Python package (uv)
│  ├─ src/matcher/{api,extract,score,taxonomy}/
│  ├─ data/skills.yaml           curated skill taxonomy (aliases, categories, relations)
│  ├─ eval/                      labeled dataset and evaluation script
│  └─ tests/
├─ profiles/example.yaml         sample profile; your real one is git-ignored
├─ docs/  SPEC.md  ARCHITECTURE.md  adr/  api/{backend,matcher}.openapi.yaml
├─ compose.yaml  .env.example
└─ .github/workflows/ci.yml      one workflow, a job per service
```

[R] Monorepo: both services and their contract change together in one PR. Two repos would
make every contract change a pair of PRs to coordinate, with no benefit for one developer.

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

scoring_versions   (id, model_version text, config_hash text, config jsonb, created_at,
                    UNIQUE (model_version, config_hash))
match_results      (id, job_id → jobs, profile_revision_id → profile_revisions,
                    scoring_version_id → scoring_versions, job_content_hash,
                    score smallint, band text, confidence text, completeness real,
                    hard_constraints text ('pass','fail','unknown'),
                    details jsonb,          -- dimensions, matched/missing/transferable skills
                    latency_ms int, created_at,
                    UNIQUE (job_id, profile_revision_id, scoring_version_id, job_content_hash))

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
6. **Traceability:** `model_version` = matcher package version + taxonomy hash + embedding
   model id and revision. `config_hash` = hash of the weights and thresholds YAML. Both go
   on every result (SPEC §6.7).
7. **Later (C):** once you have about 150+ jobs marked saved/applied/rejected, fit a logistic
   regression on the dimension scores. That replaces the hand-set weights with learned
   ones, and stays explainable. This is a new scoring version, not a new architecture.

Why not something else: embeddings alone cannot apply hard constraints or say *which* skill
is missing. Supervised ML has no labels yet. Cross-encoders and LLMs cost a lot and give up
the explainability and reproducibility the spec asks for. Rules alone miss paraphrase in
responsibilities, the one place where a small embedding model clearly adds something.

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
POST /v1/match      {profile: ProfileRevision, jobs: [NormalizedJob]}  (max N per batch)
                 →  {model_version, config_hash, results: [{job_id, score, band, confidence,
                     completeness, hard_constraints, dimensions[], skills:{matched, missing,
                     transferable, unrecognized}, explanations[]}]}
GET  /v1/version → {model_version, config_hash, config}
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
GET    /applications   PUT /applications/{job_id}  {status, note}
GET    /applications/{job_id}/events
GET    /healthz   GET /readyz (DB reachable, migrations at the latest version; matcher status reported, not required)
```
- Cursor pagination on lists. Every request gets a request ID: the backend reads
  `X-Request-ID` or generates one, and passes it on to the matcher and into every log line.

## 8. Implementation roadmap (each phase is one or more ADR-sized PRs)

| Phase | Delivers | Acceptance criteria |
|---|---|---|
| 1 Foundation | repo layout, Go `serve`/`migrate`, Python FastAPI skeleton, initial migration, compose with health checks, CI (lint, test, govulncheck, pip-audit) | `cp .env.example .env && docker compose up` brings all three up healthy. `/readyz` is green on both services. CI is green. |
| 2 Ingestion | Greenhouse + Lever adapters first, then Ashby + Himalayas; normalization, dedup, scheduler, sync runs | Syncing the same board twice gives 0 new rows (idempotent). Recorded HTTP fixtures cover each adapter. 429/5xx responses are retried with backoff. A closed posting gets `closed_at` set. A cross-source duplicate links to one job. |
| 3 Matching | profile schema + revisions, taxonomy, extraction, dimensions, scoring, explanations, eval set, Go orchestration | The eval script reports metrics, and a baseline is committed. Same inputs give a byte-identical result. A missing salary gives `unknown`, not `fail`. Results are stored with their versions. |
| 4 Applications & API | job search/filter, match listing, application lifecycle and events | Every endpoint is in the OpenAPI file and covered by contract tests. Every status transition is recorded with a timestamp. |
| 5 Hardening | metrics endpoint, timeouts/retry review, security audit, load test of match batches, docs | No open high-severity security findings. A matcher outage degrades gracefully: ingestion keeps running, results are "pending". The README alone gets a fresh clone running. |

The MVP (useful end to end) is phases 1–3 plus a minimal `GET /jobs` sorted by score.

## 9. Key decisions needing your approval (future ADRs)

| # | Decision | Recommendation | Main alternative |
|---|---|---|---|
| 1 | Repo shape | monorepo, `backend/` + `matcher/` | two repos |
| 2 | Go stack | stdlib `net/http` routing, **pgx v5** + **sqlc**, **goose** migrations embedded, `log/slog` | chi/echo; GORM (rejected: hides SQL); golang-migrate |
| 3 | Python stack | **FastAPI** + Pydantic, **uv**, pytest, ruff, mypy | Flask/Litestar; Poetry |
| 4 | Service interaction | stateless matcher, sync HTTP batches, bearer token | matcher reads the DB itself; a queue |
| 5 | Scheduling | in-process scheduler + advisory locks; work found by query | separate cron/worker container; River (Postgres job queue) |
| 6 | Schema | as in §4 (jsonb profile revisions, postings → jobs) | fully relational profile |
| 7 | Matching | hybrid A+D, banded score, separate confidence, C later | embeddings-only; supervised from day one |
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

## 11. Open questions (answers may change the design)

- **Q1.** Where will it run day to day: your laptop (syncs only while it is on), or an
  always-on host? This changes whether the scheduler needs catch-up logic.
- **Q2.** Is the public API local-only (bound to localhost, no auth), or do you want an API
  key from day one?
- **Q3.** Do you have a starting list of target companies (or their ATS board URLs)?
  If not, should Phase 2 include a one-off seed list I put together from public sources?
- **Q4.** Are you willing to grade about 150 postings for the eval set (§6)? It is the one
  thing I cannot do for you.
- **Q5.** Are English-only postings and CV fine for v1?
