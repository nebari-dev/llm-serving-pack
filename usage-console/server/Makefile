# Local dev loop for the control plane. `make dev` starts everything; the
# console then runs with `cd ../console && npm run dev:api`.
.PHONY: db db-down migrate backfill api gateway upstream traffic dev test

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
