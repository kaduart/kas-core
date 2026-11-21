-- KAS identity: people who sign in to a KAS panel (decision of 20/11/2026,
-- replacing D4 "the KAS authenticates systems only"). Services still
-- authenticate with their own token; a person is checked here and the panel
-- (a service holding identity:sessions) acts for the tenants of that person.
-- Passwords are stored only as PBKDF2-SHA256 hashes; session tokens only as
-- SHA-256 digests. E-mails are personal data: they never go to logs or events.

CREATE TABLE identity_users (
    id             TEXT        PRIMARY KEY,
    email          TEXT        NOT NULL CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 254),
    display_name   TEXT        NOT NULL CHECK (length(display_name) BETWEEN 1 AND 80),
    password_hash  TEXT        NOT NULL,
    status         TEXT        NOT NULL CHECK (status IN ('active', 'disabled')),
    failed_logins  INTEGER     NOT NULL DEFAULT 0,
    locked_until   TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT identity_users_email UNIQUE (email)
);

CREATE TABLE identity_memberships (
    user_id    TEXT        NOT NULL REFERENCES identity_users (id),
    tenant_id  TEXT        NOT NULL,
    role       TEXT        NOT NULL CHECK (role IN ('admin', 'gestor', 'atendimento', 'leitura')),
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (user_id, tenant_id)
);

CREATE TABLE identity_sessions (
    token_hash  BYTEA       PRIMARY KEY,
    user_id     TEXT        NOT NULL REFERENCES identity_users (id),
    service_id  TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked_at  TIMESTAMPTZ
);

CREATE INDEX identity_sessions_user ON identity_sessions (user_id, expires_at);
