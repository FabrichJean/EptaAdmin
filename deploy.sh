#!/usr/bin/env bash
# (Re)starts the app via pm2 — the actual build now happens in start.sh
# itself (see ecosystem.config.js), so this just needs to ask pm2 to
# (re)start the process; pm2 rebuilds from current source every time.
set -euo pipefail
cd "$(dirname "$0")"

if pm2 describe eptaadmin >/dev/null 2>&1; then
  pm2 restart ecosystem.config.js --only eptaadmin
else
  pm2 start ecosystem.config.js --only eptaadmin
fi
pm2 save >/dev/null
