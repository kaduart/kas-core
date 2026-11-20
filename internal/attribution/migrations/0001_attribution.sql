-- KAS Attribution inputs (spec "KAS por módulos", 8.2 and 8.5). Every row
-- carries tenant_id and every query filters by it. Each received version of an
-- input is a new row: nothing is rewritten or deleted by the runtime role
-- (INSERT/SELECT only, see deploy/grants-attribution.sql). This is the only
-- place in the KAS where money is stored (decision D2). Phone numbers and
-- e-mails are never stored: customers keep only a keyed fingerprint.

CREATE TABLE attribution_items (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    kind         TEXT        NOT NULL CHECK (kind IN ('customers', 'contacts', 'receipts', 'refunds')),
    external_id  TEXT        NOT NULL,
    version      INTEGER     NOT NULL CHECK (version >= 1),
    content_hash TEXT        NOT NULL,
    record       JSONB       NOT NULL,
    voided       BOOLEAN     NOT NULL,
    batch_id     TEXT        NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT attribution_items_version UNIQUE (tenant_id, kind, external_id, version)
);

CREATE INDEX attribution_items_latest ON attribution_items (tenant_id, kind, external_id, version DESC);

CREATE TABLE attribution_batches (
    tenant_id    TEXT        NOT NULL,
    batch_id     TEXT        NOT NULL,
    kind         TEXT        NOT NULL,
    request_hash TEXT        NOT NULL,
    response     JSONB       NOT NULL,
    service_id   TEXT        NOT NULL,
    actor        TEXT        NOT NULL,
    recorded_at  TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, batch_id)
);
