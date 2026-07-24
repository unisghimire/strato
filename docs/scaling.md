# Scaling Strategy

The system is designed so each growth stage is an operational change, not a rewrite.

## Stage 0 — single node (now)

Compose stack. Postgres, Redis, MinIO, one server, one worker. Good to tens of thousands of users; the API tier is already stateless.

## Stage 1 — horizontal API tier

`kubectl scale` / HPA (manifests included, 3→20 replicas). Nothing else changes because:

- No server-side session state — JWTs + Redis.
- Upload chunks go straight to object storage; any replica can receive any chunk of any session.
- Completion is serialized by the Redis lock, not by process affinity.
- Rate limits are shared in Redis, not per-process.

## Stage 2 — storage tier growth

- **Postgres**: managed HA (or CloudNativePG), then read replicas for `List`/`Search`/`GetFile` — the repository layer's read/write split point is `domain.TxManager` (reads outside transactions can route to replicas without touching use cases).
- **MinIO → S3/GCS**: `domain.BlobStore` is the seam; the MinIO client already speaks S3, so this is config.
- **Redis**: single node → Redis Cluster; the limiter and lock use single-key operations, which cluster cleanly.

## Stage 3 — millions of files, heavy metadata

- **Partitioning**: `audit_logs` by month (schema already bigserial+time, made for it); `files`/`file_versions` hash-partitioned by `owner_id` — every hot query is already owner-scoped, so partition pruning is automatic.
- **Search**: replace trigram `ILIKE` with an async-indexed search service (Meilisearch/Elastic) fed by an outbox; `FileRepository.Search` is the only method to reimplement.
- **Cache layer**: file metadata reads (hot paths: Get, List first page) behind Redis with short TTL + invalidation on write — slot it as a decorating `FileRepository`.

## Stage 4 — bytes at the edge

- **Direct-to-storage chunk uploads**: presigned PUT per chunk removes the API tier from the upload byte path entirely; the API keeps only session bookkeeping and completion. The current design anticipates this — staging keys and receipts are already the protocol.
- **CDN for public links**: put anonymous `/public/{token}` downloads behind a CDN with short-TTL signed origin requests. Private downloads keep streaming through the API (decryption + revocation semantics).
- **Multi-region**: object storage replication (S3 CRR) + a region-pinned Postgres per tenant shard; the `owner_id` scoping in every query is the tenant shard key waiting to be used.

## Worker scaling

The GC worker is single-replica by simplicity, not necessity. To scale: shard sweeps by hashing IDs into N buckets (`WHERE hash(id) % N = shard`), one replica per shard; every sweep is already idempotent and batch-limited.

## Future improvements (roadmap)

- Client SDK with parallel chunking, retry, and integrity verification built in
- Delta sync (rsync-style rolling hashes) for small edits to large files
- Per-chunk checksums verified at upload time (fields already stored)
- Transactional outbox for audit + webhooks/events (share notifications)
- Full-text content extraction & search (tika-style pipeline)
- Object-lock/WORM mode for compliance retention
- OpenID Connect federation alongside password auth
