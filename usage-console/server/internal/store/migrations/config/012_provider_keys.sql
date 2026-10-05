-- Provider keys set from the console (spec §9.1). The key itself never
-- reaches Postgres: it goes to the gateway's key store (locally an owner-only
-- env file, server/tmp/aigw/provider-keys.env) under api_key_env. Here are
-- only its non-secret prefix and when it was set (the compiled Secret carries
-- that, so a replaced key is a change to apply), plus the backend's last
-- connection test: the models it listed, or the provider's refusal.
ALTER TABLE backends
  ADD COLUMN key_prefix   text,
  ADD COLUMN key_set_at   timestamptz,
  ADD COLUMN tested_at    timestamptz,
  ADD COLUMN test_ok      boolean,
  ADD COLUMN test_message text,
  ADD CONSTRAINT backends_key_ref CHECK ((key_prefix IS NULL) = (key_set_at IS NULL) AND (key_prefix IS NULL OR api_key_env IS NOT NULL));
