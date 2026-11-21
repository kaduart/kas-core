-- Agenda mirror (phase 2, decision D-A2): bookings copied, read-only, from an
-- external system (first the local CRM replica). Mirrored bookings live in
-- sched_bookings like any other, so availability is computed the same way, but
-- with exclusive = false and client_exclusive = false: the source may hold
-- overlaps (they are measured, not refused). Nothing here stores personal data:
-- every id is an HMAC made by the importer. Reversible: rollback/0002_mirror.down.sql.

-- One row per mirrored booking: which source owns it and its original status.
CREATE TABLE sched_mirror_items (
    tenant_id         TEXT        NOT NULL,
    booking_id        TEXT        NOT NULL,
    source            TEXT        NOT NULL CHECK (source ~ '^[a-z0-9_-]{1,40}$'),
    source_status     TEXT        NOT NULL CHECK (source_status ~ '^[a-z_]{1,40}$'),
    source_updated_at TIMESTAMPTZ NOT NULL,
    mirrored_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, booking_id),
    FOREIGN KEY (tenant_id, booking_id) REFERENCES sched_bookings (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX sched_mirror_items_source ON sched_mirror_items (tenant_id, source);

-- Where the incremental read stopped: (updated_at, id) of the last row read,
-- saved in the same transaction as the rows, so a crash never skips or repeats.
CREATE TABLE sched_mirror_cursor (
    tenant_id       TEXT        NOT NULL,
    source          TEXT        NOT NULL,
    last_updated_at TIMESTAMPTZ,
    last_source_id  TEXT        NOT NULL DEFAULT '',
    full_load_at    TIMESTAMPTZ,
    reconciled_at   TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, source)
);

-- A mirrored booking missing from the source is only a candidate at first: it
-- is deleted when a second, later reconciliation still does not find it (a
-- source read can miss a row that is being rewritten). A candidate that comes
-- back is cleared.
CREATE TABLE sched_mirror_missing (
    tenant_id     TEXT        NOT NULL,
    booking_id    TEXT        NOT NULL,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    checks        INTEGER     NOT NULL DEFAULT 1,
    PRIMARY KEY (tenant_id, booking_id),
    FOREIGN KEY (tenant_id, booking_id) REFERENCES sched_mirror_items (tenant_id, booking_id) ON DELETE CASCADE
);

-- One row per run, with counters only (evidence for the comparison).
CREATE TABLE sched_mirror_runs (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    source      TEXT        NOT NULL,
    mode        TEXT        NOT NULL CHECK (mode IN ('full', 'incremental', 'reconcile')),
    started_at  TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read        INTEGER     NOT NULL DEFAULT 0,
    upserted    INTEGER     NOT NULL DEFAULT 0,
    skipped     INTEGER     NOT NULL DEFAULT 0,
    deleted     INTEGER     NOT NULL DEFAULT 0,
    candidates  INTEGER     NOT NULL DEFAULT 0,
    cleared     INTEGER     NOT NULL DEFAULT 0
);
CREATE INDEX sched_mirror_runs_tenant ON sched_mirror_runs (tenant_id, source, id);
