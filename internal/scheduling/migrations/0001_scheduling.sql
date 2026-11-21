-- KAS Agenda genérica (módulo scheduling, spec "Agenda genérica na KAS", rev 2).
-- Every row carries tenant_id and every query filters by it. Nothing here knows
-- any tenant's business: no patient, no package, no payment. A booking points to
-- its client only through client_ref, an opaque id owned by the tenant's system
-- (decision D-A4): no name, phone, e-mail or clinical data is ever stored.
--
-- Double booking is refused by the database itself (exclusion constraints), not
-- by the availability calculation, so two concurrent requests can never both win.
-- Reversible: rollback/0001_scheduling.down.sql removes everything this creates.

-- btree_gist lets the gist index compare text columns with =. It is a trusted
-- extension (PostgreSQL 13+), kept in public so it outlives any one schema;
-- the constraints name its operator class with the schema (public.gist_text_ops)
-- so they work whatever the connection's search_path is.
CREATE EXTENSION IF NOT EXISTS btree_gist WITH SCHEMA public;

CREATE TABLE sched_settings (
    tenant_id        TEXT        PRIMARY KEY,
    zone             TEXT        NOT NULL,
    holidays         TEXT[]      NOT NULL DEFAULT '{}',
    client_exclusive BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE sched_resources (
    tenant_id  TEXT    NOT NULL,
    id         TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('person', 'room', 'equipment')),
    label      TEXT    NOT NULL,
    tags       TEXT[]  NOT NULL DEFAULT '{}',
    capacity   INTEGER NOT NULL DEFAULT 1 CHECK (capacity BETWEEN 1 AND 100),
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE sched_weekly_rules (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT     NOT NULL,
    resource_id TEXT     NOT NULL,
    weekday     SMALLINT NOT NULL CHECK (weekday BETWEEN 0 AND 6),
    start_min   SMALLINT NOT NULL CHECK (start_min BETWEEN 0 AND 1439),
    end_min     SMALLINT NOT NULL CHECK (end_min BETWEEN 1 AND 1440),
    valid_from  DATE,
    valid_to    DATE,
    CHECK (end_min > start_min),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES sched_resources (tenant_id, id)
);
CREATE INDEX sched_weekly_rules_resource ON sched_weekly_rules (tenant_id, resource_id, weekday);

CREATE TABLE sched_exceptions (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    resource_id TEXT        NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL,
    reason      TEXT        NOT NULL CHECK (reason IN ('vacation', 'leave', 'off', 'holiday')),
    CHECK (ends_at > starts_at),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES sched_resources (tenant_id, id)
);
CREATE INDEX sched_exceptions_resource ON sched_exceptions (tenant_id, resource_id, starts_at);

CREATE TABLE sched_services (
    tenant_id    TEXT     NOT NULL,
    id           TEXT     NOT NULL,
    label        TEXT     NOT NULL,
    duration_min SMALLINT NOT NULL CHECK (duration_min BETWEEN 5 AND 720),
    buffer_min   SMALLINT NOT NULL DEFAULT 0 CHECK (buffer_min BETWEEN 0 AND 240),
    PRIMARY KEY (tenant_id, id)
);

CREATE TABLE sched_bookings (
    tenant_id        TEXT        NOT NULL,
    id               TEXT        NOT NULL,
    resource_id      TEXT        NOT NULL,
    client_ref       TEXT        NOT NULL CHECK (client_ref ~ '^[A-Za-z0-9._:-]{1,128}$'),
    service_id       TEXT,
    starts_at        TIMESTAMPTZ NOT NULL,
    ends_at          TIMESTAMPTZ NOT NULL,
    status           TEXT        NOT NULL CHECK (status IN ('tentative', 'scheduled', 'confirmed', 'completed', 'canceled', 'no_show')),
    series_id        TEXT,
    version          INTEGER     NOT NULL DEFAULT 1 CHECK (version >= 1),
    idempotency_key  TEXT        NOT NULL,
    -- Minutes the resource stays blocked after the booking (cleaning, notes),
    -- copied from the service when the booking is written. blocked_until is
    -- what the resource constraint uses, so the interval between bookings is
    -- enforced by the database, not only suggested by the availability API.
    buffer_min       SMALLINT    NOT NULL DEFAULT 0 CHECK (buffer_min BETWEEN 0 AND 240),
    blocked_until    TIMESTAMPTZ NOT NULL,
    -- Copied from the resource (capacity = 1) and the tenant setting when the
    -- booking is written, so the exclusion constraints can use them.
    exclusive        BOOLEAN     NOT NULL,
    client_exclusive BOOLEAN     NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id),
    CHECK (ends_at > starts_at),
    CHECK (blocked_until = ends_at + make_interval(mins => buffer_min)),
    CONSTRAINT sched_bookings_idempotency UNIQUE (tenant_id, idempotency_key),
    FOREIGN KEY (tenant_id, resource_id) REFERENCES sched_resources (tenant_id, id),
    -- One active booking per resource and instant, buffer included ('[)': with
    -- no buffer, 09:00-09:40 and 09:40-10:20 do not clash; with a 10-minute
    -- buffer the first holds the room until 09:50).
    CONSTRAINT sched_bookings_no_double_booking EXCLUDE USING gist (
        tenant_id public.gist_text_ops WITH =, resource_id public.gist_text_ops WITH =, tstzrange(starts_at, blocked_until, '[)') WITH &&)
        WHERE (exclusive AND status IN ('tentative', 'scheduled', 'confirmed')),
    -- The same client cannot be in two places at once (tenant setting).
    CONSTRAINT sched_bookings_client_no_overlap EXCLUDE USING gist (
        tenant_id public.gist_text_ops WITH =, client_ref public.gist_text_ops WITH =, tstzrange(starts_at, ends_at, '[)') WITH &&)
        WHERE (client_exclusive AND status IN ('tentative', 'scheduled', 'confirmed'))
);
CREATE INDEX sched_bookings_resource_time ON sched_bookings (tenant_id, resource_id, starts_at);

-- Every write leaves a row here in the same transaction: if the write rolls
-- back, so does its log (identifiers only, no personal data).
CREATE TABLE sched_booking_log (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id  TEXT        NOT NULL,
    booking_id TEXT        NOT NULL,
    action     TEXT        NOT NULL CHECK (action IN ('create', 'reschedule', 'cancel', 'status')),
    version    INTEGER     NOT NULL,
    actor      TEXT        NOT NULL,
    at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX sched_booking_log_booking ON sched_booking_log (tenant_id, booking_id, id);
