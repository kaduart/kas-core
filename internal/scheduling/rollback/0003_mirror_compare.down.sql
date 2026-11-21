-- Reverts migrations/0003_mirror_compare.sql. Runs first; harmless if 0003 was never applied.
DO $$
BEGIN
    IF to_regclass('scheduling_schema_migrations') IS NOT NULL THEN
        DELETE FROM scheduling_schema_migrations WHERE name = '0003_mirror_compare.sql';
    END IF;
END $$;
DROP TABLE IF EXISTS sched_mirror_comparisons;
