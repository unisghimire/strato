# Contributing to Strato

Thanks for your interest! Contributions of all sizes are welcome — bug reports, docs fixes, tests, and features.

## Getting set up

```bash
git clone https://github.com/unisghimire/strato && cd strato

# Toolchain: Go 1.23+, buf, Docker
make proto tidy       # generate gRPC stubs, resolve modules
make dev-up migrate   # start infra (Postgres/Redis/MinIO) + schema
make run              # API on :8080 / :9090
```

## Before you open a PR

```bash
make proto tidy       # if you touched .proto files
make lint             # golangci-lint + buf lint must be clean
make test             # unit tests (race detector)
make test-integration # if you touched repositories or migrations
```

## Ground rules

- **Respect the layering.** Business logic lives in `internal/usecase`, SQL in `internal/repository`, and handlers stay thin. If a handler grows an `if` about domain state, it's in the wrong place.
- **New behavior needs tests.** Use the in-memory fakes (`internal/mocks`) for use-case tests; add integration tests for new SQL.
- **Migrations are append-only.** Never edit a merged migration; add a new numbered pair.
- **Proto changes** must pass `buf lint` and `buf breaking` (CI enforces both).
- **Errors**: return wrapped domain sentinels (`domain.ErrNotFound` etc.); transport maps them. Never leak infrastructure errors to clients.
- Keep commits focused; a PR should tell one story.

## Reporting bugs / requesting features

Use the issue templates. For security vulnerabilities, **do not open a public issue** — see [SECURITY.md](SECURITY.md).

## Code style

Standard Go: `gofmt`, `golangci-lint` (config in `.golangci.yml`), doc comments on all exported symbols. Comments explain *why*, not *what*.
