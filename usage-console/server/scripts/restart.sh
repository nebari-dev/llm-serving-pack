#!/usr/bin/env bash
# Rebuild and restart parts of a running dev stack after a server change,
# without stopping the rest of it.
#
#   scripts/restart.sh [api] [warden] [ingest] [aigw]    (default: api warden)
#   make restart                                        (the same)
#   make restart WHAT="api warden ingest aigw"
#
#   api     stargate-api (:8080 REST, :8082 ext_authz), started as `serve -warden
#           http://localhost:8084`, applying the console's routing to aigw by
#           rewriting tmp/aigw/config.yaml and running `restart.sh aigw`
#   warden  Warden (:8083 ext_proc, :8084 admin)
#   ingest  receipt-ingest (:4317)
#   aigw    Agent Router (:1975) on tmp/aigw/config.yaml, written from
#           aigw/base.yaml and Postgres's routing the first time; needed after
#           editing aigw/base.yaml (then delete tmp/aigw/config.yaml first, or
#           apply from the console). Its environment is server/.env, then the
#           provider keys in tmp/aigw/provider-keys.env (set from the console).
#           Uses $AIGW (default: aigw on PATH).
#
# Run `make migrate` first if you added a migration.
#
# Processes are found by listening port and stopped by pid, never with
# `pkill -f`: `make dev-aigw` runs everything under one shell whose command
# line contains every command, and its `trap 'kill 0'` would take the whole
# stack down. A restarted process runs on its own from bin/, logging to
# tmp/<name>.log, so Ctrl-C on `make dev-aigw` no longer stops it: stop it by
# port (kill $(lsof -tiTCP:8080 -sTCP:LISTEN)).
set -euo pipefail

cd "$(dirname "$0")/.."
BIN=${BIN:-bin}
LOGS=${LOGS:-tmp}
AIGW=${AIGW:-aigw}
AIGW_CONFIG=tmp/aigw/config.yaml
AIGW_KEYS=tmp/aigw/provider-keys.env
mkdir -p "$BIN" "$LOGS"

# load_env exports KEY=VALUE lines with their values taken literally (no
# quotes or $ expansion: docker --env-file's format), skipping comments.
load_env() {
  [ -f "$1" ] || return 0
  local line
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in '' | '#'*) continue ;; esac
    export "${line%%=*}=${line#*=}"
  done <"$1"
}

what=("$@")
[ ${#what[@]} -eq 0 ] && what=(api warden)

pid_on() { lsof -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -1 || true; }

# stop_port kills whatever listens on the port, refusing to kill a shell (the
# make recipe's) in case one is ever found listening.
stop_port() {
  local pid cmd
  pid=$(pid_on "$1")
  [ -z "$pid" ] && return 0
  cmd=$(ps -o command= -p "$pid")
  case "$cmd" in */sh\ *|sh\ *|*/bash\ *|*/zsh\ *) echo "refusing to kill a shell on :$1: $cmd" >&2; exit 1 ;; esac
  kill "$pid"
}

wait_down() { for _ in $(seq 1 50); do [ -z "$(pid_on "$1")" ] && return 0; sleep 0.2; done; echo ":$1 didn't stop" >&2; exit 1; }
wait_up() { for _ in $(seq 1 120); do [ -n "$(pid_on "$1")" ] && return 0; sleep 0.25; done; echo ":$1 didn't come up; see $LOGS/$2.log" >&2; exit 1; }

for w in "${what[@]}"; do
  case "$w" in
    api)
      go build -o "$BIN/stargate-api" ./cmd/stargate-api
      stop_port 8080; wait_down 8080; wait_down 8082
      # The api applies routing by restarting aigw through this script, so it
      # needs aigw's path; without one it can diff routing but not apply it.
      restart_aigw=""
      if aigw_path=$(command -v "$AIGW"); then restart_aigw="AIGW=$aigw_path scripts/restart.sh aigw"; fi
      nohup "$BIN/stargate-api" serve -warden http://localhost:8084 \
        -aigw-config "$AIGW_CONFIG" -aigw-restart "$restart_aigw" -aigw-log "tail -n 30 $LOGS/aigw.log" >>"$LOGS/stargate-api.log" 2>&1 &
      wait_up 8080 stargate-api; wait_up 8082 stargate-api ;;
    warden)
      go build -o "$BIN/warden" ./cmd/warden
      stop_port 8083; wait_down 8083; wait_down 8084
      nohup "$BIN/warden" >>"$LOGS/warden.log" 2>&1 &
      wait_up 8083 warden; wait_up 8084 warden ;;
    ingest)
      go build -o "$BIN/receipt-ingest" ./cmd/receipt-ingest
      stop_port 4317; wait_down 4317
      nohup "$BIN/receipt-ingest" >>"$LOGS/receipt-ingest.log" 2>&1 &
      wait_up 4317 receipt-ingest ;;
    aigw)
      # Envoy listens on :1975; its parent is `aigw run`, which is what to stop.
      envoy=$(pid_on 1975)
      if [ -n "$envoy" ]; then
        parent=$(ps -o ppid= -p "$envoy" | tr -d ' ')
        case "$(ps -o command= -p "$parent")" in
          *"aigw run"*) kill "$parent" ;;
          *) stop_port 1975 ;;
        esac
        wait_down 1975
      fi
      # server/.env (gitignored) holds upstream settings aigw substitutes into
      # its config: OPENROUTER_API_KEY, LOCAL_LLM_PORT and the rest. Then the
      # provider keys set from the console, which stargate-api writes to an
      # owner-only file next to the config; they win over .env.
      if [ ! -f "$AIGW_CONFIG" ]; then
        [ -x "$BIN/stargate-api" ] || go build -o "$BIN/stargate-api" ./cmd/stargate-api
        "$BIN/stargate-api" routing write -o "$AIGW_CONFIG"
      fi
      (
        if [ -f .env ]; then set -a; . ./.env; set +a; fi
        load_env "$AIGW_KEYS"
        nohup "$AIGW" run "$AIGW_CONFIG" >>"$LOGS/aigw.log" 2>&1 &
      )
      wait_up 1975 aigw ;;
    *) echo "unknown part: $w (want api, warden, ingest or aigw)" >&2; exit 2 ;;
  esac
  echo "restarted $w"
done
