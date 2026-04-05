CREATE TABLE training_jobs (
  id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id     UUID        NOT NULL REFERENCES tenants(id),
  project_id    UUID        NOT NULL REFERENCES projects(id),
  name          TEXT        NOT NULL,
  status        TEXT        NOT NULL DEFAULT 'PENDING',
  image         TEXT        NOT NULL,
  command       TEXT[]      NOT NULL,
  args          TEXT[]      NOT NULL DEFAULT '{}',
  env           JSONB       NOT NULL DEFAULT '{}',
  num_workers   INT         NOT NULL,
  worker_cpu    TEXT        NOT NULL,
  worker_memory TEXT        NOT NULL,
  head_cpu      TEXT        NOT NULL,
  head_memory   TEXT        NOT NULL,
  rayjob_name   TEXT,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_training_jobs_tenant_status ON training_jobs (tenant_id, status);
