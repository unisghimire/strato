-- Users, refresh-token sessions, and storage quotas.

CREATE EXTENSION IF NOT EXISTS citext;    -- case-insensitive emails
CREATE EXTENSION IF NOT EXISTS pg_trgm;   -- trigram indexes for file search

-- Shared trigger to maintain updated_at without app-side bookkeeping.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext      NOT NULL UNIQUE,
    password_hash text        NOT NULL,             -- argon2id PHC string
    display_name  text        NOT NULL DEFAULT '',
    role          text        NOT NULL DEFAULT 'user' CHECK (role IN ('user', 'admin')),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);

CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Refresh-token sessions. Tokens are stored as SHA-256 digests: a DB leak
-- must not yield usable credentials. family_id groups rotations of one
-- login; reuse of a rotated token revokes the entire family.
CREATE TABLE sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id   uuid        NOT NULL,
    token_hash  bytea       NOT NULL UNIQUE,
    user_agent  text        NOT NULL DEFAULT '',
    ip          inet,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,
    replaced_by uuid REFERENCES sessions (id)
);

CREATE INDEX sessions_user_id_idx ON sessions (user_id);
CREATE INDEX sessions_family_idx  ON sessions (family_id);
CREATE INDEX sessions_expires_idx ON sessions (expires_at); -- GC sweep

-- One quota row per user, created at registration. used_bytes is maintained
-- transactionally with version creation/deletion and CHECK-guarded against
-- accounting bugs going negative.
CREATE TABLE storage_quotas (
    user_id     uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    quota_bytes bigint      NOT NULL CHECK (quota_bytes >= 0),
    used_bytes  bigint      NOT NULL DEFAULT 0 CHECK (used_bytes >= 0),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER storage_quotas_updated_at BEFORE UPDATE ON storage_quotas
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
