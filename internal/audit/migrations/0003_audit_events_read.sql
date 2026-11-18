-- Read support: optional entity reference and keyset-pagination indexes.
-- Existing rows keep NULL entity columns. Nothing is rewritten or deleted.
ALTER TABLE audit_events ADD COLUMN entity_type TEXT;
ALTER TABLE audit_events ADD COLUMN entity_id TEXT;
ALTER TABLE audit_events ADD CONSTRAINT audit_events_entity_pair
    CHECK ((entity_type IS NULL) = (entity_id IS NULL));

-- Stable order (received_at, id) within a tenant: serves the unfiltered read and
-- the "since" filter. It covers the older (tenant_id, received_at) index, which
-- is left in place so this migration drops nothing.
CREATE INDEX audit_events_tenant_order_idx ON audit_events (tenant_id, received_at, id);

-- Reads by entity, in the same stable order. Partial: rows without an entity
-- reference are not indexed here.
CREATE INDEX audit_events_tenant_entity_idx
    ON audit_events (tenant_id, entity_type, entity_id, received_at, id)
    WHERE entity_type IS NOT NULL;
