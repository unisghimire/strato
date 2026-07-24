# Performance Notes

## Memory model: everything streams

No file content is ever held in memory in full:

- **Chunk upload**: HTTP body → SHA-256 tee → MinIO `PutObject` with exact Content-Length. O(socket buffer).
- **Assembly**: staged chunks → hasher + AES-GCM segment encryptor → `io.Pipe` → MinIO. O(64 KiB segment) regardless of file size; ciphertext length is precomputed (`EncryptedSize`) so no buffering for Content-Length.
- **Download**: MinIO reader → per-segment decrypt → HTTP response. Same bound.

A 10 GiB upload and a 10 KiB upload cost the same RAM. GCM tag overhead at 64 KiB segments is ~0.02% of stored bytes.

## Concurrency

- **Parallel chunk uploads**: chunks are independent (separate staging objects + upsert receipts), so clients can push N chunks concurrently; the e2e test and API contract support out-of-order arrival.
- **Worker pool**: GC fan-out through `pkg/workerpool` (bounded goroutines, graceful drain, cancellation on shutdown timeout).
- **Distributed lock** on completion serializes only the finalize step per session across replicas — uploads themselves never contend.
- **Context cancellation** flows end-to-end: a dropped client aborts the storage stream, DB queries, and pipe producer (via `CloseWithError`); cleanup paths use `context.WithoutCancel` + timeout so canceled requests still release resources.

## Database

- **pgxpool** with configured min/max conns and lifetime; no ORM overhead, every query index-planned.
- **Keyset pagination** everywhere — `OFFSET 1000000` never happens; listing page N costs the same as page 1.
- **Single-statement invariants**: quota check+add, version MAX+1 insert, refcount updates — no read-modify-write races, no serializable isolation needed.
- **Targeted indexes**: partial unique indexes for live-sibling names, partial index for GC candidates (`WHERE ref_count = 0`, `WHERE status = 'pending'`), trigram GIN for name search, composite `(owner_id, folder_id, is_deleted)` for listings. Millions of files per user stay indexed.

## Retries & resilience

`pkg/retry`: exponential backoff with **full jitter** (avoids retry synchronization across replicas), context-aware, non-retryable short-circuit. Used for bucket bootstrap; the pattern is available for any transient infra call. Rate limiting and audit are deliberately fail-open/best-effort so Redis or audit hiccups don't take down the data path.

## Benchmarks

`make bench` measures the hot paths (numbers from a modern laptop, single core; run your own):

- `BenchmarkStreamEncrypt` / `BenchmarkStreamDecrypt`: AES-256-GCM streaming typically runs **1–3 GiB/s** with AES-NI — encryption is never the upload bottleneck; the network is.
- `BenchmarkHashPassword`: Argon2id at production parameters ~40–80 ms by design (that's the point); the dummy-verify on unknown emails keeps login timing flat.

## Known hot spots & headroom

- Assembly is single-pass but sequential per session; very large files could parallelize chunk reads into a bounded reorder buffer (interface already allows it).
- `ILIKE` trigram search is fine to ~10⁷ rows/owner; beyond that, move search to an index service (see scaling doc).
- Dedup lookup is a single unique-index probe; no measurable cost at any realistic scale.
