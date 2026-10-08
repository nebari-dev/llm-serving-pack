-- How many tokens a LiteLLM entry takes in (max_input_tokens, else
-- max_tokens), so a model a provider added, whose context starts unknown,
-- shows its entry's. 0 until the next sync fills it.
ALTER TABLE litellm_facts ADD COLUMN context_tokens integer NOT NULL DEFAULT 0;
