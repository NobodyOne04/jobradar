# JobRadar — Technical Specification

**Project type:** Personal Job Intelligence Platform  
**Architecture:** Polyglot (Go + Python)  
**Database:** PostgreSQL  
**Deployment:** Docker / Docker Compose

## 1. Project Objective

Build a production-oriented job intelligence platform that automates job discovery, ingestion, normalization, matching, and application tracking.

The primary user is a Senior Data Engineer looking for remote opportunities at international product companies and startups.

This is both:
- A practical tool for managing and optimizing a real job search.
- An engineering portfolio project demonstrating backend engineering, distributed systems, data engineering, and applied ML.

The system must be designed as a maintainable, extensible application rather than a disposable prototype.

## 2. High-Level Architecture

The system consists of two independent services.

### 2.1 Go Backend — Core Service

Go is responsible for all application-level business orchestration.

Responsibilities:

- Job source integrations.
- Job ingestion and synchronization.
- Data normalization and validation.
- Deduplication.
- Candidate profile management.
- Application tracking.
- PostgreSQL persistence.
- Matching orchestration.
- REST API.
- Scheduling and background processing.
- Service-to-service communication.
- Error handling and observability.

The Go backend is the authoritative owner of application state and business workflows.

### 2.2 Python Matching Service — ML / Intelligence

Python is responsible for job-to-candidate matching and intelligent analysis.

Responsibilities:

- Job requirement extraction.
- Candidate profile analysis.
- Skill matching.
- Semantic similarity.
- Experience and seniority analysis.
- Compensation and location compatibility analysis.
- Match scoring.
- Match explanations.
- Confidence and data completeness assessment.

The matching service must be independently deployable and communicate with the Go backend through a documented service interface.

**ML architecture is intentionally open for investigation.**

Claude must research and propose an appropriate approach before implementation.

Possible directions include:
- Classical ML.
- NLP techniques.
- Embedding-based semantic matching.
- Transformer-based models.
- Hybrid deterministic + ML architecture.
- Other approaches justified by the problem.

Do not automatically introduce LLM dependencies or unnecessarily complicated models.

The chosen architecture must balance:
- Matching quality.
- Explainability.
- Reproducibility.
- Inference latency.
- Resource consumption.
- Deployment complexity.
- Maintainability.

Claude must explain why the selected approach is preferable to alternatives.

## 3. Technology Stack

### Go Backend
- Go (latest stable version).
- PostgreSQL.
- REST API.
- Structured logging.
- Docker.
- Docker Compose.
- GitHub Actions.

Claude may propose appropriate libraries and frameworks.

### Python Matching Service
- Python (latest stable version supported by selected ML ecosystem).
- Appropriate ML/NLP libraries.
- Independent HTTP service.
- Docker deployment.
- Automated testing.

Claude is free to select the framework, model, inference architecture, and supporting libraries.

### Infrastructure
- PostgreSQL as the primary persistent storage.
- Separate containers for Go and Python services.
- Environment-based configuration.
- Persistent database volumes.
- Health checks.
- Database migrations.
- CI/CD.

Avoid introducing additional infrastructure without a demonstrated requirement.

## 4. Job Ingestion

The system should support multiple job sources through independently maintainable integrations.

Initial candidates:
- Greenhouse.
- Lever.
- Ashby.
- Himalayas.

Use publicly accessible APIs and permitted data access methods.

Requirements:
- Incremental synchronization.
- Retry policies.
- Rate-limit handling.
- Idempotent ingestion.
- Source-specific adapters.
- Raw payload preservation where useful.
- Normalized internal job representation.
- Duplicate detection across sources.

Adding a new source must not require changes to the matching engine.

## 5. Candidate Profile

The application must support a structured candidate profile derived from an existing CV.

The profile should contain:

- Professional experience.
- Seniority.
- Technical skills.
- Programming languages.
- Frameworks and infrastructure technologies.
- Data engineering experience.
- Responsibilities.
- Projects and achievements.
- Preferred employment type.
- Preferred locations and timezones.
- Salary expectations.
- Other configurable preferences.

The initial implementation can use a static, manually curated candidate profile.

However, the architecture must allow multiple profiles and profile revisions in the future.

Do not hardcode candidate-specific information into the matching engine.

## 6. Matching Engine

The matching engine is a core component of the product.

It must compare a candidate profile against a normalized job profile and produce a structured result.

Potential dimensions:

- Technical skills.
- Experience and seniority.
- Responsibilities.
- Compensation.
- Location and timezone.
- Employment type.

The scoring architecture, feature engineering, model selection, and weighting strategy are subject to technical investigation.

Requirements:

1. Results must be explainable.
2. Matching must be reproducible for the same inputs and model version.
3. Missing information must not automatically be interpreted as incompatibility.
4. Confidence and data completeness must be represented separately from the match score.
5. The system must identify matched, missing, and potentially transferable skills.
6. Scoring logic must be testable independently.
7. Model versions and scoring configurations must be traceable.
8. The architecture must support future experimentation and model replacement.

The system should distinguish between:
- Hard constraints.
- Soft preferences.
- Positive signals.
- Missing or uncertain information.

Avoid producing misleading precision in scores.

## 7. Application Tracking

The platform must support tracking the application lifecycle.

Initial statuses:

- Discovered.
- Saved.
- Applied.
- Interview.
- Offer.
- Rejected.
- Archived.

Store relevant timestamps and application notes.

A job's discovery and matching status must remain separate from its application status.

## 8. Backend API

The Go service exposes the public application API.

Initial resource groups:

- Jobs.
- Candidate profiles.
- Match results.
- Applications.
- Job sources.
- System health.

The Python service exposes only the internal matching interface.

Its API must be versioned and documented.

The two services must have clearly defined contracts and explicit error handling.

## 9. Data Storage

PostgreSQL is the primary source of truth.

Claude must propose a normalized relational schema.

Consider:
- Job sources and external identifiers.
- Canonical job records.
- Raw ingestion data.
- Candidate profiles and revisions.
- Match results.
- Model and scoring versions.
- Application tracking.
- Synchronization metadata.

Use migrations from the beginning.

Avoid storing derived information redundantly unless there is a clear reason.

## 10. Engineering Requirements

The project must follow production-oriented engineering practices.

### Code Quality
- Clear separation of responsibilities.
- Explicit interfaces and contracts.
- Maintainable package boundaries.
- No unnecessary abstractions.
- No premature optimization.

### Testing
- Unit tests.
- Integration tests.
- API contract tests.
- Database tests.
- Matching quality tests.
- Failure and recovery scenarios.

The Python matching component must have a repeatable evaluation strategy.

### Reliability
- Idempotent operations.
- Timeouts.
- Retry policies where appropriate.
- Graceful handling of dependency failures.
- Structured error responses.
- Health and readiness endpoints.

### Observability
- Structured logs.
- Request correlation.
- Service health.
- Processing metrics.
- Matching latency.
- Ingestion statistics.
- Error monitoring.

### Security
- No committed secrets.
- Environment-based configuration.
- Input validation.
- Dependency security checks.
- Internal service authentication where appropriate.
- Secure handling of candidate information.

## 11. Deployment

The entire application must be runnable locally using Docker Compose.

The deployment must include:

- Go backend.
- Python matching service.
- PostgreSQL.
- Database migrations.

Provide:
- Dockerfiles.
- Compose configuration.
- Environment example file.
- Startup instructions.
- Health checks.
- Persistent database storage.

A developer should be able to clone the repository, configure environment variables, and start the application using documented commands.

## 12. Development Phases

Claude should propose a detailed implementation roadmap.

Suggested milestones:

**Phase 0 — Architecture and Research**
- Review requirements.
- Investigate ML approaches.
- Compare potential models and libraries.
- Define service contracts.
- Propose database schema.
- Identify technical risks.
- Produce Architecture Decision Records.

**Phase 1 — Foundation**
- Repository structure.
- Go backend skeleton.
- Python service skeleton.
- PostgreSQL.
- Migrations.
- Docker Compose.
- Health checks.
- CI pipeline.

**Phase 2 — Job Ingestion**
- Initial source adapters.
- Normalization.
- Deduplication.
- Incremental synchronization.
- Persistence.

**Phase 3 — Matching Engine**
- Candidate profile.
- Job feature extraction.
- Matching implementation.
- Scoring and explanations.
- Evaluation dataset and tests.

**Phase 4 — Application Management**
- Job discovery API.
- Match results API.
- Application lifecycle.
- Search and filtering.

**Phase 5 — Production Hardening**
- Reliability improvements.
- Observability.
- Security review.
- Performance testing.
- Documentation.

Each phase must have explicit acceptance criteria.

## 13. Scope Control

Do not implement everything at once.

Prioritize a working end-to-end MVP.

Avoid:
- Unnecessary microservices.
- Kubernetes without a real deployment requirement.
- Complex event-driven infrastructure without justification.
- Premature frontend development.
- Unnecessary LLM API dependencies.
- Overengineering abstractions.
- Building custom solutions when mature libraries already solve the problem.

The project should remain useful throughout development.

## 14. Initial Deliverables

Before writing implementation code, provide:

1. Architecture proposal.
2. Go/Python service boundaries.
3. Proposed repository structure.
4. Database schema proposal.
5. Matching architecture investigation and comparison.
6. Proposed ML model and evaluation strategy.
7. API contracts.
8. Implementation roadmap.
9. Key architectural decisions and trade-offs.
10. Identified technical risks.

**Important:** Do not start implementation immediately.

Present the architecture and technical decisions for review first.

All significant architectural decisions require explicit user approval.

The user owns final architectural decisions. Claude may recommend alternatives, challenge assumptions, and explain trade-offs, but must not silently make major architectural decisions.

Once the architecture is approved, implementation proceeds incrementally, phase by phase, with tests and acceptance criteria.
