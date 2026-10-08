-- A deleted backend's prices stayed in effect, so a backend made again under
-- the same name was priced at the old rate. Deleting a backend now ends them
-- (store.DeleteBackend); this ends the ones already left open. They stay as
-- history: receipts keep the cost they were given.
UPDATE model_pricing SET effective_to = now()
WHERE effective_to IS NULL AND backend NOT IN (SELECT name FROM backends);
DELETE FROM model_pricing WHERE effective_from > now() AND backend NOT IN (SELECT name FROM backends);
DELETE FROM price_sources WHERE backend NOT IN (SELECT name FROM backends);
