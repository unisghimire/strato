-- Sharing (private grants + public links) and append-only audit log.

CREATE TABLE shares (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id       uuid        NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    owner_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Private share target...
    grantee_id    uuid REFERENCES users (id) ON DELETE CASCADE,
    -- ...or public-link token digest (raw token never stored). Exactly one
    -- of the two must be set.
    token_hash    bytea UNIQUE,
    permission    text        NOT NULL CHECK (permission IN ('viewer', 'editor', 'owner')),
    password_hash text,       -- optional argon2id gate for public links
    expires_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    revoked_at    timestamptz,
    CHECK ((grantee_id IS NULL) <> (token_hash IS NULL))
);

CREATE INDEX shares_file_idx    ON shares (file_id) WHERE revoked_at IS NULL;
CREATE INDEX shares_grantee_idx ON shares (grantee_id) WHERE revoked_at IS NULL;
CREATE INDEX shares_owner_idx   ON shares (owner_id);

-- A user may hold at most one active grant per file.
CREATE UNIQUE INDEX shares_grantee_file_uq
    ON shares (file_id, grantee_id)
    WHERE revoked_at IS NULL AND grantee_id IS NOT NULL;

-- Append-only audit trail. bigserial (not uuid): insert-ordered, compact,
-- and this table is a natural candidate for time-based partitioning later.
CREATE TABLE audit_logs (
    id            bigserial PRIMARY KEY,
    user_id       uuid, -- nullable: anonymous public-link access is audited too
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL DEFAULT '',
    ip            inet,
    user_agent    text        NOT NULL DEFAULT '',
    metadata      jsonb       NOT NULL DEFAULT '{}',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_user_time_idx     ON audit_logs (user_id, created_at DESC);
CREATE INDEX audit_logs_resource_idx      ON audit_logs (resource_type, resource_id);
CREATE INDEX audit_logs_created_idx       ON audit_logs (created_at);
