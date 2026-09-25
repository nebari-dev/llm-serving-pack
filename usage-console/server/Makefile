# Local dev loop for the control plane. `make dev` starts everything; the
# console then runs with `cd ../console && npm run dev:api`.
.PHONY: db db-down migrate backfill api gateway upstream traffic ingest warden aigw dev dev-aigw test

# Agent Router CLI: https://github.com/theagentrouter/agent-router/releases
AIGW ?= aigw

db:
	docker compose up -d --wait

db-down:
	docker compose down

migrate: db
	go run ./cmd/stargate-api migrate

backfill: db
	go run ./cmd/stargate-api backfill -days 7 -per-day 3000

api:
	go run ./cmd/stargate-api serve

gateway:
	go run ./cmd/devgateway

upstream:
	go run ./cmd/fake-openai

traffic:
	go run ./cmd/trafficgen

ingest:
	go run ./cmd/receipt-ingest

warden:
	go run ./cmd/warden

aigw:
	$(AIGW) run aigw/config.yaml

# Runs api, gateway, fake upstream and traffic together; Ctrl-C stops all.
dev: migrate
	@trap 'kill 0' INT TERM; \
	go run ./cmd/fake-openai & \
	go run ./cmd/stargate-api serve & \
	sleep 2; go run ./cmd/devgateway & \
	sleep 2; go run ./cmd/trafficgen & \
	wait

test:
	go test ./...

# Same, with the real request path: Agent Router in front of the fake upstream,
# checking keys against stargate-api's ext_authz (:8082) and budgets and rules
# with Warden's ext_proc (:8083), its access log feeding receipt-ingest.
dev-aigw: migrate
	@command -v $(AIGW) >/dev/null || { echo "aigw not found; install it or pass AIGW=/path/to/aigw"; exit 1; }
	@trap 'kill 0' INT TERM; \
	go run ./cmd/fake-openai & \
	go run ./cmd/stargate-api serve -warden http://localhost:8084 & \
	go run ./cmd/receipt-ingest & \
	go run ./cmd/warden & \
	sleep 2; $(AIGW) run aigw/config.yaml & \
	sleep 4; go run ./cmd/trafficgen -gateway http://localhost:1975 & \
	wait
