-- What each apply tried: the routes and backends the gateway didn't run as
-- they were, each at its version then ({"route/<name>": etag, "backend/<name>":
-- etag}). After a failed apply only those show as failed, and only until
-- they're edited; anything new or edited since is pending.
ALTER TABLE routing_applies ADD COLUMN attempted jsonb NOT NULL DEFAULT '{}';
