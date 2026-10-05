-- What LiteLLM's file says about each entry a pair is priced from, besides
-- price: input modalities and the provider's deprecation date. Replaced at
-- every sync.
CREATE TABLE litellm_facts (
  litellm_key      text PRIMARY KEY,
  modalities       text[] NOT NULL,
  deprecation_date date,
  synced_at        timestamptz NOT NULL
);
