-- How Warden handled the request: enforced, passthrough (kill switch),
-- fail-open or fail-closed. NULL when nothing evaluated policy.
ALTER TABLE receipts ADD COLUMN policy_mode text;
