-- Which of a key's secrets authenticated the request: the first 12 hex of the
-- secret's hash, stable when a rotation promotes the new secret. Null before
-- this was recorded, and for requests no secret authenticated.
ALTER TABLE receipts ADD COLUMN secret_id text;
