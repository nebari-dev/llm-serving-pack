-- The fake upstream (cmd/fake-openai) moved from :8090 to :18090, since
-- local model servers commonly take 8090. Backends the demo seed pointed at
-- it follow; a real backend on 8090 elsewhere is left alone.
UPDATE backends SET port = '18090'
WHERE port = '8090' AND host = '${STARGATE_HOST:-localhost}' AND prefix = '/' || name || '/v1';
