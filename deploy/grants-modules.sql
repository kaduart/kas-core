-- Runtime-role grants for the module catalog state. Run only when it is enabled
-- (-modules-store postgres), after -modules-migrate-only and after
-- deploy/grants.sql, as the migration role (the schema owner):
--
--   psql -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=kas_runtime -f deploy/grants-modules.sql
--
-- The runtime role can never delete or rewrite the history. Re-running the
-- script is safe.
\set ON_ERROR_STOP on

GRANT INSERT, SELECT, UPDATE ON :"schema".module_states TO :"runtime_role";
GRANT INSERT, SELECT ON :"schema".module_state_history TO :"runtime_role";
GRANT SELECT ON :"schema".module_schema_migrations TO :"runtime_role";
