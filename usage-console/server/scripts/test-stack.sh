#!/usr/bin/env bash
# A second control plane + gateway for the console's api-mode suite, on its
# own databases, so test runs never write keys, rules or audit rows into the
# dev stack the console shows.
#
#   scripts/test-stack.sh db        create and migrate the test databases only (make test-db)
#   scripts/test-stack.sh up        start it (creates, migrates and backfills the db the first time)
#   scripts/test-stack.sh restart   rebuild and restart its processes, keeping the data
#   scripts/test-stack.sh down      stop it
#   scripts/test-stack.sh reset     stop it, drop its databases, start fresh
#   scripts/test-stack.sh aigw      recreate the gateway container (what an apply runs)
#
# Then, from console/:  npm run test:api
#
#   db            stargate_test (config, :5433) and receipts_test (:5434), in the dev compose containers
#   stargate-api  :9080 REST, :9082 ext_authz
#   warden        :9083 ext_proc, :9084 admin
#   ingest        :9317 OTLP
#   gateway       :2975, Agent Router in Docker (two can't share a host: it binds
#                 fixed internal ports), reaching the above via host.docker.internal.
#                 It runs tmp/aigw-test/config.yaml, written from aigw/base.yaml and
#                 the test db's routing; the api applies routing there and
#                 recreates the container, with server/.env and the provider keys
#                 set from the console (tmp/aigw-test/provider-keys.env) as its
#                 environment.
#   trafficgen    0.5 rps into :2975
#
# fake-openai (:8090) and the local model server are shared with the dev
# stack: they keep no state. Logs go to tmp/test-<name>.log.
set -euo pipefail

cd "$(dirname "$0")/.."
BIN=${BIN:-bin}
LOGS=${LOGS:-tmp}
IMAGE=${AIGW_IMAGE:-envoyproxy/ai-gateway-cli:v1.1.0}
CONTAINER=stargate-test-aigw
AIGW_DIR=tmp/aigw-test
mkdir -p "$BIN" "$LOGS" "$AIGW_DIR"

export STARGATE_CONFIG_DB=postgres://stargate:stargate@localhost:5433/stargate_test?sslmode=disable
export STARGATE_RECEIPTS_DB=postgres://stargate:stargate@localhost:5434/receipts_test?sslmode=disable

pid_on() { lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -1 || true; }
wait_up() { for _ in $(seq 1 120); do [ -n "$(pid_on "$1")" ] && return 0; sleep 0.25; done; echo ":$1 didn't come up; see $LOGS/test-$2.log" >&2; exit 1; }
stop_port() {
  local pid
  pid=$(pid_on "$1")
  [ -z "$pid" ] && return 0
  case "$(ps -o command= -p "$pid")" in
    *"$BIN/"*) kill "$pid" ;;
    *) echo "refusing to stop :$1, not one of ours: $(ps -o command= -p "$pid")" >&2; exit 1 ;;
  esac
  for _ in $(seq 1 50); do [ -z "$(pid_on "$1")" ] && return 0; sleep 0.2; done
}

psql_in() { docker compose exec -T "$1" psql -U stargate -d "$2" -Atq -c "$3"; }

# run_aigw (re)creates the gateway container. Its environment is fixed when
# the container is created (`docker restart` keeps it), so an apply recreates
# it to pick up provider keys set from the console. They're merged over
# server/.env into one owner-only env file, the console's winning.
run_aigw() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  local merged="$AIGW_DIR/aigw.env"
  ( umask 077
    { if [ -f .env ]; then cat .env; echo; fi; if [ -f "$AIGW_DIR/provider-keys.env" ]; then cat "$AIGW_DIR/provider-keys.env"; fi; } |
      awk -F= '/^[A-Za-z_][A-Za-z0-9_]*=/ { if (!($1 in at)) order[++n] = $1; at[$1] = $0 } END { for (i = 1; i <= n; i++) print at[order[i]] }' >"$merged" )
  docker run -d --name "$CONTAINER" -p 2975:1975 -v "$PWD/$AIGW_DIR:/config:ro" --env-file "$merged" \
    -e STARGATE_HOST=host.docker.internal -e LOCAL_LLM_HOST=host.docker.internal \
    -e STARGATE_AUTHZ_PORT=9082 -e STARGATE_WARDEN_PORT=9083 -e STARGATE_OTLP_PORT=9317 \
    "$IMAGE" run /config/config.yaml >/dev/null
  for _ in $(seq 1 120); do
    docker logs "$CONTAINER" 2>&1 | grep -q "listening on" && break
    sleep 0.5
  done
}

ensure_db() { # container, admin db, test db
  if [ -z "$(psql_in "$1" "$2" "SELECT 1 FROM pg_database WHERE datname = '$3'")" ]; then
    psql_in "$1" "$2" "CREATE DATABASE $3"
    echo "created $3"
  fi
}

# db creates the test databases if missing and migrates them (seeding the
# demo tenant the first time). It never touches the dev databases.
db() {
  docker compose up -d --wait >/dev/null
  ensure_db configdb stargate stargate_test
  ensure_db receiptsdb receipts receipts_test
  go build -o "$BIN/stargate-api" ./cmd/stargate-api
  "$BIN/stargate-api" migrate >>"$LOGS/test-stargate-api.log" 2>&1
  echo "test databases ready: stargate_test, receipts_test"
}

up() {
  db
  for c in warden receipt-ingest trafficgen; do go build -o "$BIN/$c" "./cmd/$c"; done
  if [ -z "$(pid_on 9080)" ]; then
    nohup "$BIN/stargate-api" serve -addr :9080 -authz-addr :9082 -warden http://localhost:9084 \
      -gateway http://localhost:2975 -environment test -signing-key tmp/receipt-signing-test.pem \
      -aigw-config "$AIGW_DIR/config.yaml" -aigw-restart "scripts/test-stack.sh aigw" -aigw-log "docker logs --tail 30 $CONTAINER 2>&1" \
      >>"$LOGS/test-stargate-api.log" 2>&1 &
    wait_up 9080 stargate-api
  fi
  # The api seeds the demo tenant on first start; then give it a week of history.
  if [ "$(psql_in receiptsdb receipts_test "SELECT count(*) FROM receipts")" = 0 ]; then
    echo "backfilling a week of receipts…"
    "$BIN/stargate-api" backfill -days 7 -per-day 3000 >>"$LOGS/test-stargate-api.log" 2>&1
  fi
  if [ -z "$(pid_on 9083)" ]; then
    nohup "$BIN/warden" -addr :9083 -admin :9084 >>"$LOGS/test-warden.log" 2>&1 &
    wait_up 9083 warden
  fi
  if [ -z "$(pid_on 9317)" ]; then
    nohup "$BIN/receipt-ingest" -addr :9317 >>"$LOGS/test-receipt-ingest.log" 2>&1 &
    wait_up 9317 receipt-ingest
  fi

  [ -f "$AIGW_DIR/config.yaml" ] || "$BIN/stargate-api" routing write -o "$AIGW_DIR/config.yaml" >>"$LOGS/test-stargate-api.log" 2>&1
  [ -n "$(docker ps -q -f name="^$CONTAINER$")" ] || run_aigw

  if ! { [ -f "$LOGS/test-trafficgen.pid" ] && kill -0 "$(cat "$LOGS/test-trafficgen.pid")" 2>/dev/null; }; then
    nohup "$BIN/trafficgen" -gateway http://localhost:2975 -rps 0.5 >>"$LOGS/test-trafficgen.log" 2>&1 &
    echo $! >"$LOGS/test-trafficgen.pid"
  fi
  echo "test stack up: api :9080, gateway :2975"
}

down() {
  if [ -f "$LOGS/test-trafficgen.pid" ]; then
    kill "$(cat "$LOGS/test-trafficgen.pid")" 2>/dev/null || true
    rm -f "$LOGS/test-trafficgen.pid"
  fi
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  for p in 9080 9083 9317; do stop_port "$p"; done
  echo "test stack down"
}

case "${1:-up}" in
  db) db ;;
  up) up ;;
  down) down ;;
  restart) down; up ;;
  reset)
    down
    psql_in configdb stargate "DROP DATABASE IF EXISTS stargate_test"
    psql_in receiptsdb receipts "DROP DATABASE IF EXISTS receipts_test"
    rm -f "$AIGW_DIR/config.yaml" "$AIGW_DIR/provider-keys.env" "$AIGW_DIR/provider-keys.pending.env" "$AIGW_DIR/aigw.env"
    up ;;
  aigw) run_aigw ;;
  *) echo "usage: $0 db|up|down|restart|reset|aigw" >&2; exit 2 ;;
esac
