# Architecture

## Layering

Strato follows Clean Architecture with dependencies pointing strictly inward:

```
transport (gRPC/HTTP handlers)          ── outermost
    ↓ calls
usecase (business rules)
    ↓ depends on interfaces in
domain (ports + errors) + entity (pure types)   ── innermost
    ↑ implemented by
repository/postgres · repository/redis · storage/minio · auth/jwt
```

Rules that keep the codebase honest:

- **Handlers contain no business logic.** They parse the wire format, call one use-case method, map the result. Every handler method is ≤ ~20 lines.
- **Use cases never import infrastructure.** `internal/usecase` imports `domain`, `entity`, and `pkg/*` only. Swapping MinIO for S3 or pgx for something else touches zero business logic.
- **Repositories own SQL exclusively.** Domain errors (`ErrNotFound`, `ErrAlreadyExists`, `ErrQuotaExceeded`) are mapped from SQLSTATEs at the repository boundary; nothing above it sees a driver error.
- **One authorization gate.** `usecase.authorizeFile` is the single function deciding file access for the file, upload, and share use cases. Authorization policy cannot drift between features.

## Why a modular monolith

Two binaries (`server`, `worker`) over one codebase. At this scale, a service mesh would add latency and failure modes without buying isolation. The seams for later extraction are already interfaces (`domain.*`); promoting `usecase.UploadUseCase` into its own service is mechanical because nothing reaches around the ports.

## Data model decisions

### Content-addressed blobs with refcounting

`file_versions.blob_id → blobs`, where `blobs.checksum_sha256` is unique. Consequences:

- **Dedup is global and free.** Any upload whose plaintext SHA-256 exists skips both transfer and storage.
- **Deletion is refcounting.** Version removal decrements; the GC worker deletes objects at zero refs, after a grace window that protects in-flight uploads.
- **The race is handled.** Two concurrent uploads of the same new content both encrypt and write the same deterministic object key; one wins the `INSERT`, the loser adopts the winner's row via the unique violation. No coordination needed.

### Immutable versions

`file_versions` is append-only with `UNIQUE (file_id, version_number)` and `MAX+1` assignment inside the insert. Restore appends. This makes history auditable, concurrent writers safe (one retries on unique violation), and "undo" trivial.

### Quota accounting

`storage_quotas.used_bytes` is updated with a single statement:

```sql
UPDATE storage_quotas SET used_bytes = used_bytes + $2
WHERE user_id = $1 AND used_bytes + $2 <= quota_bytes
```

Zero rows affected ⇒ quota exceeded. Because check and add are one atomic statement, N concurrent uploads cannot jointly overshoot — verified by `TestQuotaConcurrentEnforcement` with 20 racing writers.

### Keyset pagination

All listings use `(created_at, id)` cursors, never OFFSET. Depth-independent O(log n) via composite index; stable under concurrent inserts. Cursors are opaque base64 tokens.

## Upload pipeline

1. **Init** validates name/size/checksum, checks folder ownership, advisory quota, file locks; creates a session with computed chunk count. Dedup hit → client may complete immediately.
2. **Chunks** stream via raw `PUT` (not gateway JSON — no base64 inflation, no buffering) into `staging/<session>/<index>`, hashed in flight, receipt upserted (retransmit-idempotent). Chunks can arrive in parallel and out of order.
3. **Complete** takes a Redis lock (serializes replicas), verifies receipts, then runs a single-pass stream: `staging chunks → SHA-256 tee → AES-GCM segment encryptor → io.Pipe → MinIO Put` with exact Content-Length precomputed by `EncryptedSize`. Memory use is O(segment), not O(file). Declared-checksum mismatch deletes the object and keeps the session pending for repair.
4. **Attach** runs one transaction: file row (create or reuse by name), version insert (MAX+1), blob refcount increment, current-version pointer, quota charge. Any failure rolls back all of it.

## Download pipeline

`store.Get → StreamReader (per-segment GCM verify) → client`, with `Content-Length` from metadata. Three entries share the same core: bearer-token downloads, HMAC-signed URLs (short-lived, credential-free), and anonymous public links. Signed URLs re-run authorization at redemption time, so revocation wins over URL validity.

## Garbage collection

Four idempotent, batched, crash-safe sweeps (`internal/service/gc.go`):

| Sweep | Trigger | Action |
|---|---|---|
| Expired uploads | `expires_at` past, still pending | mark expired, delete staging objects |
| Trash | soft-deleted > retention (30d default) | release blob refs + quota, purge rows — in one TX |
| Orphan blobs | `ref_count = 0` older than grace | delete object **then** row (leak-proof order), re-checking refcount to close the dedup race |
| Auth sessions | `expires_at` past | delete rows |

A worker crash mid-sweep leaves work for the next cycle; nothing requires exactly-once.

## Observability

- **Metrics**: RPC latency histograms by method/code, in-flight gauge, upload/download byte counters; Go runtime collectors. Grafana dashboard provisioned in compose.
- **Tracing**: OTel gRPC stats handler + OTLP export (Jaeger in dev). Sampling ratio configurable.
- **Logs**: `log/slog` JSON, one line per RPC with request ID; request-scoped loggers ride the context.
