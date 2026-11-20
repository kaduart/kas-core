-- Least-privilege grants for the KAS runtime role. Run as the migration role
-- (the schema owner) after the migrations, for example:
--
--   psql -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=kas_runtime -f deploy/grants.sql
--
-- The runtime role gets only what the executable needs: it can append audit
-- events, publish events, and move delivery state, and it can never delete or
-- rewrite history. Re-running the script is safe.
\set ON_ERROR_STOP on

GRANT CONNECT ON DATABASE :"DBNAME" TO :"runtime_role";
GRANT USAGE ON SCHEMA :"schema" TO :"runtime_role";

-- Audit: append and read (idempotency checks read), nothing else.
GRANT INSERT, SELECT ON :"schema".audit_events TO :"runtime_role";
GRANT SELECT ON :"schema".audit_schema_migrations TO :"runtime_role";

-- Events: the event rows are insert-only; delivery rows also change state.
GRANT INSERT, SELECT ON :"schema".outbox_events TO :"runtime_role";
GRANT INSERT, SELECT, UPDATE ON :"schema".outbox_deliveries TO :"runtime_role";
GRANT SELECT ON :"schema".events_schema_migrations TO :"runtime_role";

-- Process Studio: published definitions, role changes and history are
-- append-only; instance state, tasks, timers, commands and waits move state.
GRANT INSERT, SELECT ON :"schema".process_definitions TO :"runtime_role";
GRANT INSERT, SELECT ON :"schema".process_history TO :"runtime_role";
GRANT INSERT, SELECT ON :"schema".process_role_changes TO :"runtime_role";
GRANT INSERT, SELECT ON :"schema".process_events_received TO :"runtime_role";
GRANT SELECT ON :"schema".process_unmatched_events TO :"runtime_role";
GRANT INSERT, SELECT, UPDATE ON :"schema".process_role_assignments, :"schema".process_instances,
    :"schema".process_tasks, :"schema".process_task_submissions, :"schema".process_commands,
    :"schema".process_waits, :"schema".process_timers TO :"runtime_role";
GRANT SELECT ON :"schema".process_schema_migrations TO :"runtime_role";
