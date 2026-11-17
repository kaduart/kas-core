-- Transactional outbox. An event and one delivery row per matching consumer are
-- inserted in a single transaction. Nothing here is ever deleted automatically.
CREATE TABLE outbox_events (
    id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    contract_version TEXT        NOT NULL,
    event_id         TEXT        NOT NULL,
    tenant_id        TEXT        NOT NULL,
    service_id       TEXT        NOT NULL,
    event_type       TEXT        NOT NULL,
    correlation_id   TEXT        NOT NULL,
    occurred_at_raw  TEXT        NOT NULL,
    received_at_raw  TEXT        NOT NULL,
    occurred_at      TIMESTAMPTZ NOT NULL,
    received_at      TIMESTAMPTZ NOT NULL,
    payload          JSONB       NOT NULL,
    CONSTRAINT outbox_events_identity UNIQUE (tenant_id, service_id, event_id),
    CONSTRAINT outbox_events_payload_object CHECK (jsonb_typeof(payload) = 'object')
);

CREATE TABLE outbox_deliveries (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    event_pk        BIGINT      NOT NULL REFERENCES outbox_events (id),
    consumer_id     TEXT        NOT NULL,
    status          TEXT        NOT NULL CHECK (status IN ('pending', 'in_flight', 'delivered', 'dead')),
    attempts        INTEGER     NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL,
    locked_until    TIMESTAMPTZ,
    locked_by       TEXT,
    last_error_code TEXT,
    created_at      TIMESTAMPTZ NOT NULL,
    delivered_at    TIMESTAMPTZ,
    dead_at         TIMESTAMPTZ,
    CONSTRAINT outbox_deliveries_once UNIQUE (event_pk, consumer_id)
);

CREATE INDEX outbox_deliveries_pending_idx ON outbox_deliveries (next_attempt_at, id) WHERE status = 'pending';
CREATE INDEX outbox_deliveries_in_flight_idx ON outbox_deliveries (locked_until) WHERE status = 'in_flight';
