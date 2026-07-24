-- Resumable chunked upload sessions.

CREATE TABLE upload_sessions (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    folder_id       uuid REFERENCES folders (id) ON DELETE CASCADE,
    name            text        NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    mime_type       text        NOT NULL DEFAULT 'application/octet-stream',
    size_bytes      bigint      NOT NULL CHECK (size_bytes > 0),
    checksum_sha256 bytea       NOT NULL CHECK (octet_length(checksum_sha256) = 32),
    chunk_size      bigint      NOT NULL CHECK (chunk_size > 0),
    total_chunks    integer     NOT NULL CHECK (total_chunks > 0),
    status          text        NOT NULL DEFAULT 'pending'
                    CHECK (status IN ('pending', 'completed', 'aborted', 'expired')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL
);

CREATE INDEX upload_sessions_user_idx ON upload_sessions (user_id, status);
-- GC sweep for expired pending sessions.
CREATE INDEX upload_sessions_gc_idx ON upload_sessions (expires_at) WHERE status = 'pending';

-- One row per received chunk. Primary key doubles as the resume bitmap:
-- SELECT chunk_index WHERE session_id = $1 tells the client what to skip.
CREATE TABLE upload_chunks (
    session_id      uuid        NOT NULL REFERENCES upload_sessions (id) ON DELETE CASCADE,
    chunk_index     integer     NOT NULL CHECK (chunk_index >= 0),
    size_bytes      bigint      NOT NULL CHECK (size_bytes > 0),
    checksum_sha256 bytea       NOT NULL CHECK (octet_length(checksum_sha256) = 32),
    storage_key     text        NOT NULL,
    uploaded_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, chunk_index)
);
