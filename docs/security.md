# Security

## Threat model

Assets: user file content, account credentials, metadata. Adversaries considered: network attackers, other authenticated users (horizontal privilege escalation), stolen refresh tokens, a compromised object store, and injection via any user-controlled input. Out of scope: a fully compromised API host (it holds the KEK in memory), malicious admins.

## Encryption at rest — envelope model

- Each blob is encrypted with its **own random 256-bit DEK** using segmented AES-256-GCM (STREAM construction: 64 KiB segments, per-segment nonce = random prefix + counter, final-segment flag bound as AAD).
  - *Why segmented:* single-shot GCM needs the whole plaintext in memory; segments give constant-memory streaming and per-segment integrity.
  - *Why the final flag:* an attacker who truncates the ciphertext at a segment boundary is detected — the last segment must carry the authenticated final marker. Covered by `TestStreamTamperDetection`.
- DEKs are **wrapped by the master KEK** (AES-256-GCM) and stored next to blob metadata. The object store never sees a key or plaintext; Postgres never sees content.
- **KEK rotation** = decrypt-rewrap all `wrapped_dek` values (small, fast) — not re-encrypting content. In production the KEK comes from a secret manager/KMS via env; the repo ships only a dev placeholder.

## Passwords & tokens

- **Argon2id** with OWASP-recommended parameters (m=64 MiB, t=3, p=2), PHC-encoded so parameters travel with the hash; `NeedsRehash` upgrades weak hashes transparently at next login.
- **Login timing**: unknown emails burn a dummy Argon2id verification, so response time does not reveal account existence.
- **Access tokens**: HS256 JWTs, 15 min, algorithm pinned at verification (no alg-confusion), issuer + expiry enforced.
- **Refresh tokens**: 256-bit random, stored only as SHA-256 digests, **rotated on every use**. Presenting a rotated token is treated as theft: the entire session family is revoked and the event audited. Logout revokes the family; expired sessions are GC'd.
- **Public link tokens**: 256-bit random, digest-stored, optional Argon2id password gate, optional expiry, instant revocation.

## Authorization & IDOR

- All file access flows through one gate (`usecase.authorizeFile`). Callers with no relationship to a resource receive `NOT_FOUND` — indistinguishable from a nonexistent ID, so IDs cannot be probed. `PERMISSION_DENIED` is reserved for principals who legitimately know the resource exists.
- Upload sessions, folders, and shares apply the same "hide, don't deny" rule.
- Permission lattice (viewer < editor < owner) is enforced in the use case, re-checked against share expiry/revocation both in SQL and in code.

## Injection & input handling

- **SQLi**: every query is parameterized pgx; no string-built SQL with user data anywhere (dynamic fragments are static SQL text only — values always ride placeholders).
- **Directory traversal**: file/folder names are validated once (`validateName`): no `/`, `\`, NUL, control chars, `.`/`..`, 1–255 chars — and names are *never* interpreted as paths; storage keys are derived from UUIDs and checksums, not names.
- **Content-type**: user MIME types are parsed and defaulted to `application/octet-stream` when invalid; downloads set `X-Content-Type-Options: nosniff` and `Content-Disposition: attachment`.
- **Request bodies**: gRPC messages capped at 4 MiB; chunk PUTs capped at chunk size via `http.MaxBytesReader`.

## Transport & headers

TLS terminates at the ingress (cert-manager) or, optionally, directly on the listeners (`server.tls`). All raw HTTP responses carry `nosniff`, `X-Frame-Options: DENY`, `no-referrer`, and a deny-all CSP. Downloads are `Cache-Control: private, no-store`.

## Rate limiting

Redis sliding-window log (sorted set, atomic Lua): no fixed-window boundary bursts. Keyed by user token (prefix) or client IP; login/register get a stricter budget against credential stuffing. Redis failure **fails open** with logging — availability over strictness, and the trade-off is explicit in code.

## Audit logging

Append-only `audit_logs` records auth events (including failed logins and token-reuse detections), file lifecycle, share lifecycle, and anonymous public-link access, with IP/UA from request context. Audit writes are best-effort and survive request cancellation; compliance-grade delivery would move to a transactional outbox (documented trade-off in `usecase.Auditor`).

## Signed URLs

Application-layer HMAC-SHA256 over `(file_id, user_id, expiry)`, constant-time verified. S3 presigned URLs are deliberately *not* used for downloads: blobs are ciphertext, so bytes must flow through the API for decryption — which also means authorization is re-evaluated at redemption (revoked shares beat unexpired URLs).

## Known limitations (honest list)

- Symmetric JWT secret: fine while issuer = verifier; a gateway-verified deployment should move to asymmetric keys.
- The API host holds the KEK in memory; an HSM/KMS `Decrypt` call per DEK unwrap would remove that.
- Rate limiter fail-open is a deliberate availability choice; flip to fail-closed for hostile environments.
- Public "editor" links allow anonymous version uploads by design; deploys can restrict links to viewer-only in policy.
