ALTER TABLE api_tokens
  ADD COLUMN token_prefix TEXT NOT NULL DEFAULT '';

CREATE INDEX idx_api_tokens_prefix ON api_tokens (token_prefix);
