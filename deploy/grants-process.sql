-- Runtime-role grants for the Process Studio engine. Run only when it is
-- enabled (-process-store postgres), after -process-migrate-only and after
-- deploy/grants.sql, as the migration role (the schema owner):
--
--   psql -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=kas_runtime -f deploy/grants-process.sql
--
-- The runtime role can never delete or rewrite definitions, role changes,
-- received events or history. Re-running the script is safe.
\set ON_ERROR_STOP on

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
