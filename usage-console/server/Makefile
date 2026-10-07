# Local dev loop for the control plane. `make dev` starts everything; the
# console then runs with `cd ../console && npm run dev:api`.
.PHONY: db db-down migrate backfill api keycloak keycloak-down dev-oidc gateway upstream traffic ingest warden aigw dev dev-aigw test restart test-db test-stack test-stack-restart test-stack-down test-stack-reset

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
	go run ./cmd/stargate-api serve $(API_FLAGS)

# Console sign-in against the local Keycloak (compose profile auth, :8180,
# realm nebari). AUTH=oidc on api, dev, dev-aigw or restart signs the console
# in with it (`make keycloak` first); without it, dev mode: everyone is
# dev@localhost, owner. `make dev-oidc` is both. The console runs with
# `npm run dev:api` either way.
OIDC_FLAGS = -oidc-issuer http://localhost:8180/realms/nebari -oidc-client-id stargate-console -console-url http://localhost:5173
ifeq ($(AUTH),oidc)
API_FLAGS += $(OIDC_FLAGS)
export STARGATE_OIDC_CLIENT_SECRET ?= stargate-dev-secret
endif

keycloak:
	docker compose --profile auth up -d --wait keycloak

keycloak-down:
	docker compose --profile auth stop keycloak

dev-oidc: keycloak
	$(MAKE) dev AUTH=oidc

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

# A fresh dev database gets the real seed: the local model server and team
# names, nothing simulated. SEED=demo seeds the simulated demo instead (the
# fake upstream's backends, made-up keys and history), and makes dev and
# dev-aigw start the fake upstream and the traffic generator for it. The seed
# only applies to a database that has no tenant yet.
SEED ?= real
export STARGATE_SEED := $(SEED)

# Runs api, the dev gateway and, with SEED=demo, the fake upstream and
# traffic together; Ctrl-C stops all.
dev: migrate
	@trap 'kill 0' INT TERM; \
	$(if $(filter demo,$(SEED)),go run ./cmd/fake-openai &) \
	go run ./cmd/stargate-api serve $(API_FLAGS) & \
	sleep 2; go run ./cmd/devgateway & \
	$(if $(filter demo,$(SEED)),sleep 2; go run ./cmd/trafficgen &) \
	wait

test:
	go test ./...

# Same, with the real request path: Agent Router in front of the backends,
# checking keys against stargate-api's ext_authz (:8082) and budgets and rules
# with Warden's ext_proc (:8083), its access log feeding receipt-ingest.
dev-aigw: migrate tmp/aigw/config.yaml
	@command -v $(AIGW) >/dev/null || { echo "aigw not found; install it or pass AIGW=/path/to/aigw"; exit 1; }
	@trap 'kill 0' INT TERM; \
	$(if $(filter demo,$(SEED)),go run ./cmd/fake-openai &) \
	go run ./cmd/stargate-api serve $(API_FLAGS) -warden http://localhost:8084 -aigw-config tmp/aigw/config.yaml \
	  -aigw-restart "AIGW=$$(command -v $(AIGW)) scripts/restart.sh aigw" -aigw-log "tail -n 30 tmp/aigw.log" & \
	go run ./cmd/receipt-ingest & \
	go run ./cmd/warden & \
	sleep 2; $(AIGW) run tmp/aigw/config.yaml & \
	$(if $(filter demo,$(SEED)),sleep 4; go run ./cmd/trafficgen -gateway http://localhost:1975 &) \
	wait

# Rebuild and restart parts of a running stack after a server change, found by
# port, without stopping the rest: WHAT is any of api warden ingest aigw.
WHAT ?= api warden
restart:
	AIGW=$(AIGW) STARGATE_API_FLAGS="$(API_FLAGS)" scripts/restart.sh $(WHAT)

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
