ALTER TABLE tenants
  DROP COLUMN IF EXISTS cpu_quota,
  DROP COLUMN IF EXISTS memory_quota;
