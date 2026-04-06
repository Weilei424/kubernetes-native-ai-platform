CREATE TABLE model_records (
  id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id                    UUID        NOT NULL REFERENCES tenants(id),
  project_id                   UUID        NOT NULL REFERENCES projects(id),
  name                         TEXT        NOT NULL,
  mlflow_registered_model_name TEXT        NOT NULL,
  created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
