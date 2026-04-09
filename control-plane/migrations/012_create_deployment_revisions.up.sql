CREATE TABLE deployment_revisions (
  id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
  deployment_id    UUID        NOT NULL REFERENCES deployments(id),
  revision_number  INT         NOT NULL,
  model_version_id UUID        NOT NULL REFERENCES model_versions(id),
  status           TEXT        NOT NULL DEFAULT 'active',
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (deployment_id, revision_number)
);

CREATE INDEX idx_deployment_revisions_deployment_id ON deployment_revisions (deployment_id);
