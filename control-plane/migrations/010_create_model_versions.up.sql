CREATE TABLE model_versions (
  id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  model_record_id UUID        NOT NULL REFERENCES model_records(id),
  tenant_id       UUID        NOT NULL REFERENCES tenants(id),
  version_number  INT         NOT NULL,
  mlflow_run_id   TEXT        NOT NULL,
  source_run_id   UUID        NOT NULL REFERENCES training_runs(id),
  artifact_uri    TEXT        NOT NULL,
  status          TEXT        NOT NULL DEFAULT 'candidate',
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (model_record_id, version_number)
);

CREATE INDEX idx_model_versions_model_record_id ON model_versions (model_record_id);
CREATE INDEX idx_model_versions_tenant_id ON model_versions (tenant_id);
