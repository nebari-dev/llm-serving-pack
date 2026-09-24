-- Control-plane config (spec §5.2). Ids are short text slugs so they line up
-- with the console's existing references ('support', 'k1', 'r3').

CREATE TABLE tenants (
  id   text PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE teams (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES tenants(id),
  name        text NOT NULL,
  cost_center text NOT NULL
);

CREATE TABLE model_catalog (
  id       text PRIMARY KEY,
  display  text NOT NULL,
  provider text NOT NULL,
  family   text NOT NULL,
  context  integer NOT NULL
);

CREATE TABLE model_pricing (
  model_id        text NOT NULL REFERENCES model_catalog(id),
  in_per_m        numeric(12,6) NOT NULL,
  out_per_m       numeric(12,6) NOT NULL,
  cached_per_m    numeric(12,6) NOT NULL,
  reasoning_per_m numeric(12,6) NOT NULL,
  effective_from  timestamptz NOT NULL,
  effective_to    timestamptz,
  PRIMARY KEY (model_id, effective_from)
);

CREATE TABLE model_aliases (
  alias  text PRIMARY KEY,   -- exact name or a trailing-* prefix pattern
  target text NOT NULL REFERENCES model_catalog(id)
);

CREATE TABLE backends (
  name            text PRIMARY KEY,
  tenant_id       text NOT NULL REFERENCES tenants(id),
  ordinal         integer NOT NULL,
  provider        text NOT NULL,
  region          text NOT NULL,
  provenance      text NOT NULL CHECK (provenance IN ('console','git','adopted')),
  sync_state      text NOT NULL CHECK (sync_state IN ('synced','applying','failed','drift')),
  source_ref      text,
  models          text[] NOT NULL,
  health          text NOT NULL CHECK (health IN ('healthy','degraded','down')),
  p50_ms          integer NOT NULL,
  error_rate      numeric(6,2) NOT NULL,
  capture_content boolean NOT NULL DEFAULT false
);

CREATE TABLE routes (
  name            text PRIMARY KEY,
  tenant_id       text NOT NULL REFERENCES tenants(id),
  ordinal         integer NOT NULL,
  match           text NOT NULL,
  targets         jsonb NOT NULL,
  fallback        text[] NOT NULL,
  provenance      text NOT NULL,
  sync_state      text NOT NULL,
  capture_content boolean NOT NULL DEFAULT false
);

CREATE TABLE budgets (
  id         text PRIMARY KEY,
  tenant_id  text NOT NULL REFERENCES tenants(id),
  scope_type text NOT NULL CHECK (scope_type IN ('team','key','project')),
  scope      text NOT NULL,
  period     text NOT NULL DEFAULT 'monthly',
  cap_usd    numeric(14,2) NOT NULL,
  on_exceed  text NOT NULL CHECK (on_exceed IN ('warn','throttle','block'))
);

CREATE TABLE api_keys (
  id              text PRIMARY KEY,
  tenant_id       text NOT NULL REFERENCES tenants(id),
  name            text NOT NULL,
  prefix          text NOT NULL,
  hash            text NOT NULL UNIQUE,       -- sha256 of the secret; the secret is never stored
  next_hash       text UNIQUE,                -- new secret during a rotation overlap
  rotate_until    timestamptz,
  team_id         text NOT NULL REFERENCES teams(id),
  project         text NOT NULL,
  allowed_models  text[] NOT NULL,
  allowed_regions text[] NOT NULL,
  budget_id       text REFERENCES budgets(id),
  expires_at      date,
  status          text NOT NULL CHECK (status IN ('active','revoked','rotating')),
  created_at      timestamptz NOT NULL DEFAULT now(),
  revoked_at      timestamptz
);

CREATE TABLE policy_rules (
  id          text PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES tenants(id),
  ordinal     integer NOT NULL,
  name        text NOT NULL,
  description text NOT NULL,
  mode        text NOT NULL CHECK (mode IN ('enforce','monitor','draft')),
  fail_mode   text NOT NULL CHECK (fail_mode IN ('open','closed')),
  version     integer NOT NULL,
  "when"      jsonb NOT NULL,
  "then"      jsonb NOT NULL
);

CREATE TABLE detectors (
  id        text PRIMARY KEY,
  tenant_id text NOT NULL REFERENCES tenants(id),
  name      text NOT NULL,
  kind      text NOT NULL,
  threshold numeric(4,2) NOT NULL,
  hits_24h  integer NOT NULL DEFAULT 0,
  fp        integer NOT NULL DEFAULT 0
);

-- Every mutation writes one row here in the same transaction (spec §6).
CREATE TABLE audit_log (
  id          bigserial PRIMARY KEY,
  tenant_id   text NOT NULL REFERENCES tenants(id),
  ts          timestamptz NOT NULL DEFAULT now(),
  actor       text NOT NULL,
  action      text NOT NULL,
  target      text NOT NULL,
  target_kind text NOT NULL,
  target_id   text,
  before      jsonb,
  after       jsonb,
  effect      text,
  effect_tone text CHECK (effect_tone IN ('good','bad','neutral')),
  source      text NOT NULL CHECK (source IN ('console','git'))
);
CREATE INDEX audit_log_ts ON audit_log (tenant_id, ts DESC);
