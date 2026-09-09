#!/usr/bin/env bash
set -euo pipefail

ROOT="/Users/orie/dev/multica"
ENV_FILE="$ROOT/.env"
LOG_DIR="$ROOT/.launchd-logs"
mkdir -p "$LOG_DIR"

# Prefer the repo-tested Node 22 runtime for Next/React dev server stability.
# Homebrew currently exposes a newer Node on osx-server; keep it as fallback,
# but put NVM Node 22 first when available.
NODE22_BIN="/Users/orie/.nvm/versions/node/v22.20.0/bin"
if [ -x "$NODE22_BIN/node" ]; then
  export PATH="/Users/orie/.local/bin:$NODE22_BIN:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
else
  export PATH="/Users/orie/.local/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin"
fi

if [ ! -f "$ENV_FILE" ]; then
  echo "Missing env file: $ENV_FILE" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1090
. "$ENV_FILE"
set +a

cd "$ROOT"

wait_for_docker() {
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  if [ -d "/Applications/Docker.app" ]; then
    open -gja Docker || true
  fi
  for _ in $(seq 1 90); do
    if docker info >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "Docker is not ready" >&2
  return 1
}

start_database() {
  wait_for_docker

  # Prefer Orie's existing named local database container so we keep the current data volume.
  if docker container inspect multica-postgres >/dev/null 2>&1; then
    docker start multica-postgres >/dev/null
  else
    docker compose up -d postgres
  fi

  local container="multica-postgres"
  if ! docker container inspect "$container" >/dev/null 2>&1; then
    container="multica-postgres-1"
  fi

  for _ in $(seq 1 60); do
    if docker exec "$container" pg_isready -U "${POSTGRES_USER:-multica}" -d postgres >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done

  docker exec "$container" pg_isready -U "${POSTGRES_USER:-multica}" -d postgres >/dev/null

  local exists
  exists="$(docker exec "$container" psql -U "${POSTGRES_USER:-multica}" -d postgres -Atqc "SELECT 1 FROM pg_database WHERE datname = '${POSTGRES_DB:-multica}'" 2>/dev/null || true)"
  if [ "$exists" != "1" ]; then
    docker exec "$container" psql -U "${POSTGRES_USER:-multica}" -d postgres -v ON_ERROR_STOP=1 -c "CREATE DATABASE \"${POSTGRES_DB:-multica}\"" >/dev/null
  fi
}

start_database

cd "$ROOT/server"
go run ./cmd/migrate up

cd "$ROOT"

cleanup() {
  for pid in "${SERVER_PID:-}" "${WEB_PID:-}" "${DAEMON_BOOT_PID:-}"; do
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      pkill -TERM -P "$pid" 2>/dev/null || true
      kill "$pid" 2>/dev/null || true
    fi
  done
}
trap cleanup EXIT INT TERM

ts() {
  date '+%Y-%m-%dT%H:%M:%S%z'
}

restart_self() {
  local reason="$1"
  echo "[$(ts)] $reason; restarting stack in-process" >&2
  cleanup
  sleep 3
  exec "$0"
}

echo "[$(ts)] starting Multica stack with node=$(command -v node 2>/dev/null || echo missing) $(node -v 2>/dev/null || true), pnpm=$(command -v pnpm 2>/dev/null || echo missing)" >&2

(cd "$ROOT/server" && go run ./cmd/server >>"$LOG_DIR/server.log" 2>&1) &
SERVER_PID=$!

pnpm dev:web >>"$LOG_DIR/web.log" 2>&1 &
WEB_PID=$!

# Start/restart local Multica daemon after the API is healthy. Do not fail the app if daemon auth needs attention.
(
  for _ in $(seq 1 60); do
    if curl -fsS "http://localhost:${PORT:-8080}/health" >/dev/null 2>&1; then
      break
    fi
    sleep 1
  done
  if curl -fsS "http://localhost:${PORT:-8080}/health" >/dev/null 2>&1; then
    multica daemon restart >>"$LOG_DIR/daemon.log" 2>&1 || true
  fi
) &
DAEMON_BOOT_PID=$!

echo "[$(ts)] started server pid=$SERVER_PID web pid=$WEB_PID daemon-bootstrap pid=$DAEMON_BOOT_PID" >&2

# launchd only supervises this parent script. If either the API or the Next.js
# web child dies, or if a child wrapper remains up while its HTTP endpoint goes
# dark, exit non-zero so KeepAlive restarts the whole stack instead of leaving
# Tailscale Serve proxying to a missing frontend.
api_failures=0
web_failures=0
while true; do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then
    wait "$SERVER_PID" || status=$?
    status=${status:-1}
    restart_self "Multica API server exited with status $status"
  fi
  if ! kill -0 "$WEB_PID" 2>/dev/null; then
    wait "$WEB_PID" || status=$?
    status=${status:-1}
    restart_self "Multica web server exited with status $status"
  fi

  if curl -fsS --max-time 2 "http://127.0.0.1:${PORT:-8080}/health" >/dev/null 2>&1; then
    api_failures=0
  else
    api_failures=$((api_failures + 1))
  fi

  if curl -fsSI --max-time 2 "http://127.0.0.1:3001/login" >/dev/null 2>&1; then
    web_failures=0
  else
    web_failures=$((web_failures + 1))
  fi

  if [ "$api_failures" -ge 5 ]; then
    restart_self "Multica API health failed $api_failures consecutive checks"
  fi
  if [ "$web_failures" -ge 5 ]; then
    restart_self "Multica web health failed $web_failures consecutive checks"
  fi

  sleep 2
done
