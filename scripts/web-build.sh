#!/usr/bin/env bash
# Build the production web bundle for the launchd-served stack.
#
# The build goes into apps/web/.next-staging while the running server keeps
# serving apps/web/.next untouched, then the two are swapped. Records the
# commit it built in apps/web/.next/fork-build-sha so scripts/launchd-start.sh
# skips the build when nothing changed.
#
#   scripts/web-build.sh            build if HEAD changed since the last build
#   scripts/web-build.sh --force    build even on a dirty tree at the same HEAD
#   scripts/web-build.sh --deploy   build (forced) and restart the launchd stack
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

WEB="apps/web"
LIVE="$WEB/.next"
STAGING="$WEB/.next-staging"
MARKER="$LIVE/fork-build-sha"
HEAD_SHA="$(git rev-parse HEAD 2>/dev/null || echo unknown)"
MODE="${1:-}"

if [ "$MODE" = "" ] && [ -f "$LIVE/BUILD_ID" ] && [ -f "$MARKER" ] \
  && [ "$(cat "$MARKER")" = "$HEAD_SHA" ]; then
  echo "web build up to date ($HEAD_SHA)"
  exit 0
fi

echo "building web bundle for $HEAD_SHA into $STAGING"
set -a
# shellcheck disable=SC1091
. "$ROOT/.env"
set +a
unset NEXT_DIST_DIR
rm -rf "$STAGING"
NEXT_DIST_DIR=.next-staging CI=true pnpm --filter @multica/web build
# next build rewrites the tracked next-env.d.ts; put it back so a deploy
# build never leaves the checkout dirty.
# It also adds the staging distDir to tsconfig.json's include list.
git checkout -q -- "$WEB/next-env.d.ts" "$WEB/tsconfig.json" 2>/dev/null || true

# Swap. A server still running on the old directory keeps its open files but
# must be restarted before it lazily loads anything new, which --deploy does.
rm -rf "$WEB/.next-old"
if [ -d "$LIVE" ]; then mv "$LIVE" "$WEB/.next-old"; fi
mv "$STAGING" "$LIVE"
echo "$HEAD_SHA" > "$MARKER"
rm -rf "$WEB/.next-old"
echo "web build done"

if [ "$MODE" = "--deploy" ]; then
  echo "restarting ai.multica.stack"
  launchctl kickstart -k "gui/$(id -u)/ai.multica.stack"
fi
