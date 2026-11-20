-- KAS Process Studio. Published definitions and history are append-only for the
-- runtime role (INSERT/SELECT only, see deploy/grants.sql). Nothing is deleted
-- automatically. Every row carries tenant_id and every query filters by it.

CREATE TABLE process_definitions (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    process_key  TEXT        NOT NULL,
    version      INTEGER     NOT NULL CHECK (version >= 1),
    definition   JSONB       NOT NULL,
    checksum     TEXT        NOT NULL,
    publish_id   TEXT        NOT NULL,
    published_by TEXT        NOT NULL,
    published_at TIMESTAMPTZ NOT NULL,
    CONSTRAINT process_definitions_version UNIQUE (tenant_id, process_key, version),
    CONSTRAINT process_definitions_publish UNIQUE (tenant_id, publish_id)
);

CREATE TABLE process_role_changes (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          TEXT        NOT NULL,
    role               TEXT        NOT NULL,
    change_id          TEXT        NOT NULL,
    assignment_version BIGINT      NOT NULL,
    members            TEXT[]      NOT NULL,
    valid_from         TIMESTAMPTZ NOT NULL,
    changed_by         TEXT        NOT NULL,
    recorded_at        TIMESTAMPTZ NOT NULL,
    CONSTRAINT process_role_changes_change UNIQUE (tenant_id, change_id),
    CONSTRAINT process_role_changes_version UNIQUE (tenant_id, role, assignment_version)
);

CREATE TABLE process_role_assignments (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          TEXT        NOT NULL,
    role               TEXT        NOT NULL,
    actor_id           TEXT        NOT NULL,
    valid_from         TIMESTAMPTZ NOT NULL,
    valid_to           TIMESTAMPTZ,
    assignment_version BIGINT      NOT NULL
);
CREATE INDEX process_role_assignments_lookup ON process_role_assignments (tenant_id, role, actor_id);

CREATE TABLE process_instances (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     TEXT        NOT NULL,
    process_key   TEXT        NOT NULL,
    version       INTEGER     NOT NULL,
    business_ref  TEXT        NOT NULL,
    start_key     TEXT        NOT NULL,
    status        TEXT        NOT NULL CHECK (status IN ('running', 'completed')),
    current_step  TEXT        NOT NULL,
    step_visit    INTEGER     NOT NULL DEFAULT 0,
    state_version INTEGER     NOT NULL DEFAULT 0,
    last_outcome  TEXT,
    result        TEXT,
    started_by    TEXT        NOT NULL,
    started_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    CONSTRAINT process_instances_start UNIQUE (tenant_id, start_key)
);
CREATE UNIQUE INDEX process_instances_one_active ON process_instances (tenant_id, process_key, business_ref) WHERE status = 'running';
CREATE INDEX process_instances_ref ON process_instances (tenant_id, business_ref) WHERE status = 'running';

CREATE TABLE process_tasks (
    id            BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id     TEXT        NOT NULL,
    instance_id   BIGINT      NOT NULL REFERENCES process_instances (id),
    step          TEXT        NOT NULL,
    visit         INTEGER     NOT NULL,
    roles         TEXT[]      NOT NULL,
    outcomes      TEXT[]      NOT NULL,
    due_at        TIMESTAMPTZ,
    cutoff_at     TIMESTAMPTZ,
    status        TEXT        NOT NULL CHECK (status IN ('open', 'resolved', 'expired', 'cancelled')),
    outcome       TEXT,
    submission_id BIGINT,
    created_at    TIMESTAMPTZ NOT NULL,
    closed_at     TIMESTAMPTZ,
    CONSTRAINT process_tasks_visit UNIQUE (instance_id, step, visit)
);
CREATE INDEX process_tasks_open ON process_tasks (tenant_id, status);

CREATE TABLE process_task_submissions (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          TEXT        NOT NULL,
    task_id            BIGINT      NOT NULL REFERENCES process_tasks (id),
    submission_key     TEXT        NOT NULL,
    outcome            TEXT        NOT NULL,
    actor_id           TEXT        NOT NULL,
    role               TEXT        NOT NULL,
    service_id         TEXT        NOT NULL,
    assignment_version BIGINT      NOT NULL,
    occurred_at        TIMESTAMPTZ NOT NULL,
    received_at        TIMESTAMPTZ NOT NULL,
    report_delayed     BOOLEAN     NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('pending', 'applied', 'late', 'superseded', 'rejected')),
    status_reason      TEXT,
    CONSTRAINT process_task_submissions_key UNIQUE (tenant_id, submission_key)
);
CREATE INDEX process_task_submissions_task ON process_task_submissions (task_id, status);

CREATE TABLE process_commands (
    id                 BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id          TEXT        NOT NULL,
    instance_id        BIGINT      NOT NULL REFERENCES process_instances (id),
    step               TEXT        NOT NULL,
    command_key        TEXT        NOT NULL,
    command_type       TEXT        NOT NULL,
    business_ref       TEXT        NOT NULL,
    generation         INTEGER     NOT NULL,
    status             TEXT        NOT NULL CHECK (status IN ('requested', 'confirmed', 'rejected', 'unknown')),
    request_attempt    INTEGER     NOT NULL,
    status_queries     INTEGER     NOT NULL DEFAULT 0,
    result_reason      TEXT,
    result_occurred_at TIMESTAMPTZ,
    requested_at       TIMESTAMPTZ NOT NULL,
    updated_at         TIMESTAMPTZ NOT NULL,
    CONSTRAINT process_commands_key UNIQUE (tenant_id, command_key),
    CONSTRAINT process_commands_generation UNIQUE (instance_id, step, generation)
);

CREATE TABLE process_waits (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    instance_id BIGINT      NOT NULL REFERENCES process_instances (id),
    step        TEXT        NOT NULL,
    visit       INTEGER     NOT NULL,
    wait_key    TEXT        NOT NULL,
    event_types TEXT[]      NOT NULL,
    business_ref TEXT       NOT NULL,
    due_at      TIMESTAMPTZ,
    cutoff_at   TIMESTAMPTZ,
    status      TEXT        NOT NULL CHECK (status IN ('open', 'matched', 'expired', 'cancelled')),
    opened_at   TIMESTAMPTZ NOT NULL,
    closed_at   TIMESTAMPTZ,
    CONSTRAINT process_waits_key UNIQUE (tenant_id, wait_key)
);
CREATE INDEX process_waits_open ON process_waits (tenant_id, business_ref) WHERE status = 'open';

CREATE TABLE process_events_received (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    service_id   TEXT        NOT NULL,
    event_id     TEXT        NOT NULL,
    event_type   TEXT        NOT NULL,
    business_ref TEXT        NOT NULL,
    wait_key     TEXT,
    occurred_at  TIMESTAMPTZ NOT NULL,
    received_at  TIMESTAMPTZ NOT NULL,
    outcome      TEXT        NOT NULL CHECK (outcome IN ('matched', 'interrupt', 'wait_closed', 'stale', 'late', 'ambiguous', 'unmatched', 'wait_type_mismatch')),
    instance_id  BIGINT,
    CONSTRAINT process_events_received_identity UNIQUE (tenant_id, service_id, event_id)
);

-- Events that found no wait or interrupt: kept, never discarded, and alerted.
CREATE VIEW process_unmatched_events AS
    SELECT * FROM process_events_received WHERE outcome IN ('unmatched', 'ambiguous');

CREATE TABLE process_timers (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id    TEXT        NOT NULL,
    instance_id  BIGINT      NOT NULL REFERENCES process_instances (id),
    kind         TEXT        NOT NULL CHECK (kind IN ('task_deadline', 'wait_deadline', 'command_reply')),
    ref_id       BIGINT      NOT NULL,
    due_at       TIMESTAMPTZ NOT NULL,
    cutoff_at    TIMESTAMPTZ NOT NULL,
    fire_at      TIMESTAMPTZ NOT NULL,
    status       TEXT        NOT NULL CHECK (status IN ('pending', 'done', 'cancelled')),
    executed_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX process_timers_due ON process_timers (fire_at, id) WHERE status = 'pending';

CREATE TABLE process_history (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   TEXT        NOT NULL,
    instance_id BIGINT      NOT NULL,
    recorded_at TIMESTAMPTZ NOT NULL,
    kind        TEXT        NOT NULL,
    step        TEXT,
    service_id  TEXT,
    actor_id    TEXT,
    role        TEXT,
    detail      JSONB       NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX process_history_instance ON process_history (tenant_id, instance_id, id);
