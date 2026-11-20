-- Runtime-role grants for KAS Attribution. Run only when it is enabled
-- (-attribution-store postgres), after -attribution-migrate-only and after
-- deploy/grants.sql, as the migration role (the schema owner):
--
--   psql -v ON_ERROR_STOP=1 -v schema=public -v runtime_role=kas_runtime -f deploy/grants-attribution.sql
--
-- Inputs and batches are append-only for the runtime role: a changed input is
-- a new version, never an UPDATE or DELETE. Re-running the script is safe.
\set ON_ERROR_STOP on

GRANT INSERT, SELECT ON :"schema".attribution_items TO :"runtime_role";
GRANT INSERT, SELECT ON :"schema".attribution_batches TO :"runtime_role";
GRANT SELECT ON :"schema".attribution_schema_migrations TO :"runtime_role";
