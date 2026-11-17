-- One event per (tenant_id, service_id, event_id). This fails, and the migration
-- is rolled back, if the table already holds duplicate identities from the
-- 0001-only era. Data is never deleted automatically: resolve duplicates by hand
-- in the disposable database, then run the migration again.
CREATE UNIQUE INDEX audit_events_identity_uidx ON audit_events (tenant_id, service_id, event_id);
