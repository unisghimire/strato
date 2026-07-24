# API Reference

Strato exposes the same services over gRPC (`:9090`, reflection enabled) and REST (`:8080`, via grpc-gateway). OpenAPI specs are generated into `docs/openapi/` by `make proto`.

## Authentication

`Authorization: Bearer <access_token>` on every call except register/login/refresh/logout and public links. Access tokens are 15-minute JWTs; refresh via `POST /v1/auth/refresh`, which **rotates** the refresh token — always store the new one. Re-using a rotated token revokes the whole session (theft response) and returns `401`.

## Endpoints

### Auth

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/auth/register` | `{email, password (≥10 chars), display_name}` |
| POST | `/v1/auth/login` | → `{access_token, refresh_token, access_token_expires_at}` |
| POST | `/v1/auth/refresh` | rotate refresh token |
| POST | `/v1/auth/logout` | revoke session family |
| GET | `/v1/auth/me` | profile + quota usage |

### Uploads (chunked, resumable)

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/uploads` | `{name, folder_id?, mime_type, size_bytes, checksum_sha256}` → `{session_id, chunk_size, total_chunks, already_exists}` |
| PUT | `/v1/uploads/{session}/chunks/{index}` | **raw body** = chunk bytes; parallel-safe; idempotent per chunk |
| GET | `/v1/uploads/{session}` | `{received_chunks: [...]}` — resend only the gaps |
| POST | `/v1/uploads/{session}/complete` | verify → dedup → encrypt → new version |
| DELETE | `/v1/uploads/{session}` | abort + discard staging |

If `already_exists` is true (dedup hit), skip chunks and call complete directly — zero bytes transferred.

### Files & folders

| Method | Path | Notes |
|---|---|---|
| GET | `/v1/files/{id}` | metadata |
| GET | `/v1/files?folder_id=&include_deleted=&page.page_size=&page.page_token=` | keyset-paginated |
| GET | `/v1/files:search?query=&mime_type=` | trigram substring search |
| GET | `/v1/files/{id}/content` | **streaming download** (bearer or signed query) |
| GET | `/v1/files/{id}:downloadUrl` | short-lived signed URL (credential-free GET) |
| POST | `/v1/files/{id}:rename` · `:move` · `:restore` · `:lock` · `:unlock` | |
| DELETE | `/v1/files/{id}` | soft delete (30-day trash) |
| GET | `/v1/files/{id}/versions` | history, newest first |
| POST | `/v1/files/{id}/versions/{vid}:restore` | append-only restore |
| POST | `/v1/folders` · GET/DELETE `/v1/folders/{id}` | folders must be empty to delete |

### Sharing

| Method | Path | Notes |
|---|---|---|
| POST | `/v1/shares` | `{file_id, grantee_email, permission, expires_at?}` |
| POST | `/v1/shares:publicLink` | `{file_id, permission (viewer/editor), expires_at?, password?}` — token returned **once** |
| GET | `/v1/shares?file_id=` | shares you created |
| DELETE | `/v1/shares/{id}` | revoke (immediate) |
| GET | `/public/{token}?password=` | anonymous download; password also accepted via `X-Share-Password` header |

## Pagination

Requests take `page.page_size` (clamped to 200, default 50) and `page.page_token`. Responses carry `page.next_page_token`; empty means done. Tokens are opaque — never construct or parse them.

## Permissions

`viewer` ⊂ `editor` ⊂ `owner`: viewer reads/downloads; editor also writes new versions, renames, locks; owner also deletes, shares, revokes. Advisory locks block *all other* writers including the file owner, until released by the holder, owner, or an admin.

## Error model

| gRPC | HTTP | Meaning |
|---|---|---|
| `NOT_FOUND` | 404 | missing **or not yours** — existence is never disclosed |
| `ALREADY_EXISTS` | 409 | duplicate email / sibling name / share |
| `INVALID_ARGUMENT` | 400 | validation failure (message says which) |
| `UNAUTHENTICATED` | 401 | bad/missing/expired/reused token |
| `PERMISSION_DENIED` | 403 | you have access, but not this much |
| `RESOURCE_EXHAUSTED` | 429/507 | rate limit / storage quota |
| `FAILED_PRECONDITION` | 412 | file locked, upload incomplete/expired, checksum mismatch, share password |

Internal errors return no detail — details go to server logs keyed by request ID.
