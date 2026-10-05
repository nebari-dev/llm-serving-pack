# Local dev loop for the control plane. `make dev` starts everything; the
# console then runs with `cd ../console && npm run dev:api`.
.PHONY: db db-down migrate backfill api gateway upstream traffic ingest warden aigw dev dev-aigw test restart test-db test-stack test-stack-restart test-stack-down test-stack-reset

# Agent Router CLI: https://github.com/theagentrouter/agent-router/releases
AIGW ?= aigw

# Names the build in the console's version footer (go run doesn't stamp VCS info).
VERSION ?= $(shell git describe --always --dirty 2>/dev/null || echo dev)
export GOFLAGS := -ldflags=-X=github.com/jbouder/stargate/server/internal/buildinfo.Version=$(VERSION)

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

# aigw runs tmp/aigw/config.yaml: aigw/base.yaml plus the console's routing.
# It's written from Postgres the first time; the console's applies rewrite it.
tmp/aigw/config.yaml:
	go run ./cmd/stargate-api routing write -o $@

aigw: tmp/aigw/config.yaml
	$(AIGW) run tmp/aigw/config.yaml

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
dev-aigw: migrate tmp/aigw/config.yaml
	@command -v $(AIGW) >/dev/null || { echo "aigw not found; install it or pass AIGW=/path/to/aigw"; exit 1; }
	@trap 'kill 0' INT TERM; \
	go run ./cmd/fake-openai & \
	go run ./cmd/stargate-api serve -warden http://localhost:8084 -aigw-config tmp/aigw/config.yaml \
	  -aigw-restart "AIGW=$$(command -v $(AIGW)) scripts/restart.sh aigw" -aigw-log "tail -n 30 tmp/aigw.log" & \
	go run ./cmd/receipt-ingest & \
	go run ./cmd/warden & \
	sleep 2; $(AIGW) run tmp/aigw/config.yaml & \
	sleep 4; go run ./cmd/trafficgen -gateway http://localhost:1975 & \
	wait

# Rebuild and restart parts of a running stack after a server change, found by
# port, without stopping the rest: WHAT is any of api warden ingest aigw.
WHAT ?= api warden
restart:
	AIGW=$(AIGW) scripts/restart.sh $(WHAT)

# A second stack on its own databases (stargate_test, receipts_test) for the
# console's api-mode suite: api :9080, gateway :2975 (Agent Router in Docker).
# Then `cd ../console && npm run test:api`. Restart it after a server change.
# test-db only creates and migrates its databases.
test-db:
	scripts/test-stack.sh db

test-stack:
	scripts/test-stack.sh up

test-stack-restart:
	scripts/test-stack.sh restart

test-stack-down:
	scripts/test-stack.sh down

test-stack-reset:
	scripts/test-stack.sh reset
