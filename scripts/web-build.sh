#!/usr/bin/env bash
# Build the production web bundle for the launchd-served stack.
#
# Records the commit it was built from in apps/web/.next/fork-build-sha so
# scripts/launchd-start.sh can skip the build when nothing changed. Run it
# by hand after editing web code on a dirty tree, or pass --force.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

MARKER="apps/web/.next/fork-build-sha"
HEAD_SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
FORCE="${1:-}"

if [ "$FORCE" != "--force" ] && [ -f apps/web/.next/BUILD_ID ] && [ -f "$MARKER" ] \
  && [ "$(cat "$MARKER")" = "$HEAD_SHA" ]; then
  echo "web build up to date ($HEAD_SHA)"
  exit 0
fi

echo "building web bundle for $HEAD_SHA"
set -a
# shellcheck disable=SC1091
. "$ROOT/.env"
set +a
CI=true pnpm --filter @multica/web build
echo "$HEAD_SHA" > "$MARKER"
echo "web build done"
