CREATE TABLE audit_events (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    contract_version TEXT        NOT NULL,
    event_id         TEXT        NOT NULL,
    tenant_id        TEXT        NOT NULL,
    service_id       TEXT        NOT NULL,
    actor            TEXT        NOT NULL,
    action           TEXT        NOT NULL,
    resource         TEXT        NOT NULL,
    result           TEXT        NOT NULL CHECK (result IN ('success', 'failure', 'denied')),
    correlation_id   TEXT        NOT NULL,
    occurred_at_raw  TEXT        NOT NULL,
    received_at_raw  TEXT        NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    received_at      TIMESTAMPTZ NOT NULL
);

CREATE INDEX audit_events_tenant_received_idx ON audit_events (tenant_id, received_at);
CREATE INDEX audit_events_correlation_idx ON audit_events (correlation_id);
