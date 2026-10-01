#!/usr/bin/env bash
# Build the Go binary, swap it in atomically, then (re)start via pm2.
set -euo pipefail
cd "$(dirname "$0")"

go build -o eptaadmin-linux.new .
mv eptaadmin-linux.new eptaadmin-linux   # mv avoids "Text file busy"

if pm2 describe eptaadmin >/dev/null 2>&1; then
  pm2 restart ecosystem.config.js --only eptaadmin
else
  pm2 start ecosystem.config.js --only eptaadmin
fi
pm2 save >/dev/null
