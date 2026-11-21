-- Reverts migrations/0002_mirror.sql: drops the mirror bookkeeping and the
-- mirrored bookings themselves. Runs before 0001's rollback and does nothing
-- when 0002 was never applied.
DO $$
BEGIN
    IF to_regclass('sched_mirror_items') IS NOT NULL THEN
        DELETE FROM sched_bookings b USING sched_mirror_items m WHERE b.tenant_id = m.tenant_id AND b.id = m.booking_id;
    END IF;
    IF to_regclass('scheduling_schema_migrations') IS NOT NULL THEN
        DELETE FROM scheduling_schema_migrations WHERE name = '0002_mirror.sql';
    END IF;
END $$;
DROP TABLE IF EXISTS sched_mirror_runs;
DROP TABLE IF EXISTS sched_mirror_missing;
DROP TABLE IF EXISTS sched_mirror_cursor;
DROP TABLE IF EXISTS sched_mirror_items;
