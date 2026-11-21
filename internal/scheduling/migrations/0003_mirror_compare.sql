-- Comparison history (phase 3A): one row per comparison of the mirror with its
-- source, counts only (no id, no personal data). Reversible:
-- rollback/0003_mirror_compare.down.sql.
CREATE TABLE sched_mirror_comparisons (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id       TEXT        NOT NULL,
    source          TEXT        NOT NULL CHECK (source ~ '^[a-z0-9_-]{1,40}$'),
    window_from     DATE        NOT NULL,
    window_to       DATE        NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resource_days   INTEGER     NOT NULL,
    equal_days      INTEGER     NOT NULL,
    import_errors   INTEGER     NOT NULL,
    clinical_rules  INTEGER     NOT NULL,
    unexplained     INTEGER     NOT NULL,
    patterns_equal  INTEGER     NOT NULL,
    patterns_total  INTEGER     NOT NULL,
    CHECK (window_to >= window_from)
);
CREATE INDEX sched_mirror_comparisons_tenant ON sched_mirror_comparisons (tenant_id, source, id);
