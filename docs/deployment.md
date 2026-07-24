# Deployment Guide

## Prerequisites

- Go 1.23+, [buf](https://buf.build) (proto codegen), Docker + Compose
- `make proto tidy` once after cloning (generated stubs are not committed)

## Local development

```bash
cp .env.example .env
./scripts/gen-secrets.sh >> .env       # real random secrets
make dev-up                            # postgres, redis, minio, prometheus, grafana, jaeger
make proto tidy migrate                # codegen + schema
make run                               # API on :8080 (REST) / :9090 (gRPC) / :9100 (metrics)
make worker                            # GC worker (separate terminal)
make seed                              # optional demo users
```

## Full stack in containers

```bash
docker compose -f docker/docker-compose.yml --profile app up --build
```

The `app` profile adds `migrate` (runs once), `server`, and `worker` on top of the infrastructure services. The image is a three-stage build (buf codegen → static Go build → distroless nonroot).

## Configuration

Everything in `configs/config.yaml` is overridable via `STRATO_<SECTION>_<KEY>` env vars; with `--config ""` the binaries run from env alone (the K8s mode). Required secrets:

| Variable | Format | Generate |
|---|---|---|
| `STRATO_AUTH_JWT_SECRET` | ≥32 chars | `openssl rand -base64 48` |
| `STRATO_ENCRYPTION_MASTER_KEY` | base64, exactly 32 bytes | `openssl rand -base64 32` |
| `STRATO_POSTGRES_PASSWORD` / `STRATO_S3_SECRET_KEY` | any | `openssl rand -hex 16` |

Config validation is fail-fast: a missing/short secret aborts startup with a precise message.

## Kubernetes

Manifests in `deployments/k8s/` (namespace, configmap, secret template, server Deployment+Service+HPA+PDB, worker, ingress):

```bash
kubectl apply -f deployments/k8s/namespace.yaml
# Populate secrets properly (ExternalSecrets / SealedSecrets / SOPS) — the
# committed secret.yaml is a template with CHANGE-ME values.
kubectl apply -f deployments/k8s/
```

Operational properties built in:

- **Migrations as init container** — schema is current before the server accepts traffic; concurrent replicas are safe (golang-migrate takes a Postgres advisory lock).
- **Probes**: `/healthz` (liveness) and `/readyz` (checks Postgres + Redis) gate rollout; `maxUnavailable: 0` + PDB `minAvailable: 2` keep capacity during deploys and node drains.
- **Graceful shutdown**: SIGTERM → gRPC GracefulStop + HTTP drain within `server.shutdown_timeout` (20 s < 30 s grace period).
- **Security context**: distroless, nonroot, read-only rootfs, no capabilities, seccomp RuntimeDefault.
- **Ingress**: TLS via cert-manager; request buffering off and body size unlimited for streaming uploads.
- **Worker**: single replica (sweeps are idempotent; one avoids duplicate work). Server HPA scales 3→20 on CPU.

Postgres/Redis/MinIO in production belong on managed services or dedicated operators (CloudNativePG, Redis Operator, MinIO Operator / real S3) — app manifests here assume those exist at the DNS names in the ConfigMap.

## Upgrades & migrations

- Migrations are forward-only in deploys; `cmd/migrate --down` exists for development rollback.
- The app image is versioned by git tag in CI; roll back by re-applying the previous image tag (schema migrations must stay backward-compatible one release, the standard expand/contract discipline).
