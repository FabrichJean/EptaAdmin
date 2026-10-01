#!/usr/bin/env bash
# Entry point pm2 actually runs (see ecosystem.config.js) — builds the
# current source before every start/restart so pm2 can never launch a
# stale binary just because someone ran `pm2 restart` directly instead of
# ./deploy.sh. Builds to a temp file and mv's it into place (mv is atomic
# and avoids "text file busy" if anything still has the old binary open).
set -euo pipefail
cd "$(dirname "$0")"

go build -o eptaadmin-linux.new .
mv eptaadmin-linux.new eptaadmin-linux

exec ./eptaadmin-linux
