CREATE TABLE training_runs (
  id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  job_id         UUID        NOT NULL REFERENCES training_jobs(id),
  tenant_id      UUID        NOT NULL REFERENCES tenants(id),
  status         TEXT        NOT NULL DEFAULT 'PENDING',
  mlflow_run_id  TEXT,
  started_at     TIMESTAMPTZ,
  finished_at    TIMESTAMPTZ,
  failure_reason TEXT,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_training_runs_job_id ON training_runs (job_id);
