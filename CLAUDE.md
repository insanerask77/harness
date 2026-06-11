# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

Harness Open Source (formerly Gitness): an all-in-one DevOps platform — code hosting, CI pipelines, Gitspaces (hosted dev environments), and artifact registries. Go backend (module `github.com/harness/gitness`) + React/TypeScript frontend in `web/`. Single binary built from `cmd/gitness`.

## Commands

### Backend (Go)

```bash
make build                  # generate wire code + build ./gitness binary
make test                   # run all go tests with coverage (excludes some registry suites)
go test ./app/api/controller/repo/...          # run tests for one package
go test ./git/... -run TestParseDiff           # run a single test
make lint                   # golangci-lint, only changes vs HEAD~ (CI mode)
make lint-local             # lint only changes vs merge-base with main
make lint-full              # lint everything
make format                 # goimports + gci (import grouping: standard, gitness, default)
make generate               # regenerate wire DI code (cmd/gitness/wire_gen.go)
make force-wire             # force wire regeneration
make generate-mocks         # mockery mocks (registry controllers)
```

Run the server locally (after `make build`; reads env from `.local.env`):

```bash
./gitness server .local.env    # serves on localhost:3000
```

First-time setup: `make dep`, `make tools`, `make init` (installs git hooks from `.githooks/`).

### Frontend (web/)

```bash
cd web
yarn install
yarn dev           # webpack dev server + typed-scss watch
yarn build         # production build (required before building the binary embeds it)
yarn test          # jest
yarn lint          # eslint (custom rules in scripts/eslint-rules)
yarn typecheck     # tsc
yarn check:all     # typecheck + lint + prettier + test
```

### Regenerating the UI API client after adding/changing REST APIs

```bash
./gitness swagger > web/src/services/code/swagger.yaml
cd web && yarn services
```

## Architecture

### Dependency injection (Google Wire)

The entire system is wired via Google Wire in `cmd/gitness/wire.go` → generated `cmd/gitness/wire_gen.go`. **When you add a new controller, service, or store, you must register its provider/WireSet in `wire.go` and run `make generate`.** Most packages expose a `WireSet` in a `wire.go` file alongside the implementation.

### Request flow: router → handler → controller → service/store

- `app/router/` — chi v5 router; registers routes per domain and middleware (auth, logging).
- `app/api/handler/<domain>/` — thin HTTP layer: parse params (`app/api/request` helpers), call controller, render response (`app/api/render`).
- `app/api/controller/<domain>/` — business logic: fetch aggregates, check permissions (e.g. `getRepoCheckAccess`), delegate to services and stores. One file per operation.
- `app/services/` — domain services (protection rules, webhooks, merge, notifications, gitspaces, ...).
- `app/store/` — store interfaces; `app/store/database/` — SQL implementations (sqlx-style, no ORM) wrapped in `store/database/dbtx` for transactions.

Handlers, controllers, and OpenAPI definitions (`app/api/openapi/`) mirror each other per domain — a new endpoint typically touches all three plus the router.

### Database

Two supported drivers: PostgreSQL and SQLite. Migrations are raw SQL in `app/store/database/migrate/postgres/` and `app/store/database/migrate/sqlite/` (`NNNN_name.up.sql` / `.down.sql`). **A schema change needs migrations for both dialects.** The registry has its own migrations under `registry/app/store/database/migrate/`.

### Git layer (`git/`)

Abstraction over git that shells out to the `git` CLI (no libgit2). `git/api/` exposes high-level operations; `git/command/` builds commands; `git/hook/` handles server-side hooks. The app talks to it through the `git.Interface` service, never directly.

### Events (`events/`, `stream/`, `app/events/`)

Generic producer/consumer framework in `events/` backed by `stream/` (Redis Streams in production, in-memory fallback for single-node). Typed event definitions and reporters/readers per category live in `app/events/<category>/` (git, pullreq, repo, pipeline, ...). Services subscribe via ReaderFactories registered in wire.

### Artifact registry (`registry/`)

Mostly self-contained sub-application (OCI/Docker, Maven, NPM, Cargo, Go, etc.) with its own API, store, migrations, and jobs under `registry/app/`. OCI conformance tests are shell-based: `make ar-conformance-test`.

### Other notable packages

- `types/` — shared domain types and enums used across all layers.
- `job/` — background/cron job scheduler.
- `blob/` — pluggable blob storage (filesystem, GCS).
- `lock/`, `pubsub/`, `cache/` — Redis-backed with in-memory fallbacks (the pattern across infra packages: Redis for multi-node, memory for single-node).
- `ssh/` — SSH server for git operations.
- `cli/` — implementations of the `gitness` CLI subcommands (server, migrate, swagger, user, ...).

### Frontend (`web/`)

React 18 + TypeScript, Webpack, Harness UICore + Blueprint. API clients are **generated** with restful-react from swagger specs in `web/src/services/*/swagger.yaml` — don't hand-edit generated `index.tsx` service files; regenerate them (`yarn services`). i18n strings have generated types (`yarn strings`).

## Conventions

- Commit messages follow `type: [CODE-XXXX] description` (Jira ticket reference), e.g. `fix: [CODE-5591] handle non-unique merge bases gracefully`.
- Go import ordering is enforced by gci: standard → `github.com/harness/gitness` → external → blank → dot (`make format` applies it).
- A pre-commit hook (`.githooks/pre-commit`, installed via `make init`) runs checks on staged Go files.
