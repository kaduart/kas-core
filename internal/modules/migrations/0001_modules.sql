-- KAS module catalog: the last applied contract state of each module for each
-- tenant, and every change to it. The configuration file is the source of
-- truth; these tables only remember what was applied so a change can be
-- announced once on the outbox. Every row carries tenant_id. Nothing here holds
-- business data, personal data or money.

CREATE TABLE module_states (
    tenant_id      TEXT        NOT NULL,
    module_key     TEXT        NOT NULL,
    status         TEXT        NOT NULL CHECK (status IN ('active', 'paused', 'absent')),
    config_version TEXT        NOT NULL,
    changed_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, module_key)
);

-- Append-only for the runtime role (see deploy/grants-modules.sql).
CREATE TABLE module_state_history (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id      TEXT        NOT NULL,
    module_key     TEXT        NOT NULL,
    from_status    TEXT        NOT NULL CHECK (from_status IN ('active', 'paused', 'absent')),
    to_status      TEXT        NOT NULL CHECK (to_status IN ('active', 'paused', 'absent')),
    config_version TEXT        NOT NULL,
    recorded_at    TIMESTAMPTZ NOT NULL
);

CREATE INDEX module_state_history_tenant ON module_state_history (tenant_id, module_key, id);
