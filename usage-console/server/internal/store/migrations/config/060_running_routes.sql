-- The routes each apply put in front of the gateway: Warden matches requests
-- against the last applied set (what's running) to know which route, and so
-- whether it captures content (§9.2). NULL for applies before this.
ALTER TABLE routing_applies ADD COLUMN routes jsonb;
