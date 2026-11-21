-- Reverts migrations/0001_scheduling.sql. Run only with the migration role and
-- only after the scheduling data is no longer needed: it deletes every table.
-- The btree_gist extension is left in place because other schemas may use it.
DROP TABLE IF EXISTS sched_booking_log;
DROP TABLE IF EXISTS sched_bookings;
DROP TABLE IF EXISTS sched_services;
DROP TABLE IF EXISTS sched_exceptions;
DROP TABLE IF EXISTS sched_weekly_rules;
DROP TABLE IF EXISTS sched_resources;
DROP TABLE IF EXISTS sched_settings;
DROP TABLE IF EXISTS scheduling_schema_migrations;
