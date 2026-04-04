CREATE TABLE platform_events (
  id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id   UUID        NOT NULL REFERENCES tenants(id),
  entity_type TEXT        NOT NULL,
  entity_id   UUID        NOT NULL,
  event_type  TEXT        NOT NULL,
  payload     JSONB,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ON platform_events (tenant_id, entity_type, entity_id);
