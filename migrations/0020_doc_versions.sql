-- Every previous state of a platform section (bs_kanban, bs_crm, …), so an
-- overwrite by a stale browser or a lost edit can be undone from the platform.
-- The newest 100 states per section are kept.
CREATE TABLE IF NOT EXISTS platform_doc_versions (
    scope       TEXT        NOT NULL,
    key         TEXT        NOT NULL,
    version     INTEGER     NOT NULL,
    value       TEXT        NOT NULL,
    deleted     BOOLEAN     NOT NULL DEFAULT false,
    updated_at  TIMESTAMPTZ NOT NULL,
    updated_by  TEXT        NOT NULL DEFAULT '',
    saved_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (scope, key, version)
);
