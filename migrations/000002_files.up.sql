-- Folders, content-addressed blobs, files, and immutable version history.

CREATE TABLE folders (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id   uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    parent_id  uuid REFERENCES folders (id) ON DELETE CASCADE, -- NULL = root
    name       text        NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE TRIGGER folders_updated_at BEFORE UPDATE ON folders
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Sibling names must be unique; COALESCE folds NULL parents (root) into one
-- namespace because UNIQUE treats NULLs as distinct.
CREATE UNIQUE INDEX folders_sibling_name_uq
    ON folders (owner_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), name)
    WHERE deleted_at IS NULL;

CREATE INDEX folders_parent_idx ON folders (parent_id) WHERE deleted_at IS NULL;

-- Content-addressed blob store index. One row per unique plaintext SHA-256;
-- identical uploads share a blob (deduplication). ref_count is maintained
-- transactionally with version rows; the GC worker deletes storage objects
-- for rows that reach zero.
CREATE TABLE blobs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    checksum_sha256 bytea       NOT NULL UNIQUE CHECK (octet_length(checksum_sha256) = 32),
    size_bytes      bigint      NOT NULL CHECK (size_bytes >= 0),
    storage_key     text        NOT NULL,
    -- Data-encryption key for this blob, wrapped by the master KEK
    -- (envelope encryption). Rotating the KEK re-wraps DEKs only.
    wrapped_dek     bytea       NOT NULL,
    ref_count       integer     NOT NULL DEFAULT 0 CHECK (ref_count >= 0),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX blobs_gc_idx ON blobs (created_at) WHERE ref_count = 0;

CREATE TABLE files (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_id           uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    folder_id          uuid REFERENCES folders (id) ON DELETE SET NULL, -- NULL = root
    name               text        NOT NULL CHECK (length(name) BETWEEN 1 AND 255),
    mime_type          text        NOT NULL DEFAULT 'application/octet-stream',
    current_version_id uuid, -- FK added below (circular with file_versions)
    locked_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    locked_at          timestamptz,
    is_deleted         boolean     NOT NULL DEFAULT false,
    deleted_at         timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER files_updated_at BEFORE UPDATE ON files
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE UNIQUE INDEX files_sibling_name_uq
    ON files (owner_id, COALESCE(folder_id, '00000000-0000-0000-0000-000000000000'::uuid), name)
    WHERE is_deleted = false;

CREATE INDEX files_owner_folder_idx ON files (owner_id, folder_id, is_deleted);
-- Trigram index makes ILIKE '%query%' search usable at millions of rows.
CREATE INDEX files_name_trgm_idx ON files USING gin (name gin_trgm_ops);

-- Append-only version history. Rows are never updated or deleted while the
-- file exists; "restore version N" appends a new row referencing N's blob.
CREATE TABLE file_versions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    file_id        uuid        NOT NULL REFERENCES files (id) ON DELETE CASCADE,
    version_number integer     NOT NULL CHECK (version_number > 0),
    blob_id        uuid        NOT NULL REFERENCES blobs (id),
    size_bytes     bigint      NOT NULL CHECK (size_bytes >= 0),
    created_by     uuid        NOT NULL REFERENCES users (id),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (file_id, version_number)
);

CREATE INDEX file_versions_blob_idx ON file_versions (blob_id);

ALTER TABLE files
    ADD CONSTRAINT files_current_version_fk
    FOREIGN KEY (current_version_id) REFERENCES file_versions (id);
