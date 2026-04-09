CREATE TABLE deployments (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id        UUID        NOT NULL REFERENCES tenants(id),
  project_id       UUID        NOT NULL REFERENCES projects(id),
  model_record_id  UUID        NOT NULL REFERENCES model_records(id),
  model_version_id UUID        NOT NULL REFERENCES model_versions(id),
  name             TEXT        NOT NULL,
  namespace        TEXT        NOT NULL DEFAULT 'default',
  status           TEXT        NOT NULL DEFAULT 'pending',
  desired_replicas INT         NOT NULL DEFAULT 1,
  serving_endpoint TEXT,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);

CREATE INDEX idx_deployments_tenant_id ON deployments (tenant_id);
CREATE INDEX idx_deployments_project_id ON deployments (project_id);
CREATE INDEX idx_deployments_model_record_id ON deployments (model_record_id);
CREATE INDEX idx_deployments_status ON deployments (status) WHERE status IN ('pending', 'provisioning');
