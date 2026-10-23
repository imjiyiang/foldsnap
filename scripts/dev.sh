#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [ ! -d apps/web/node_modules ]; then npm ci --prefix apps/web; fi
mkdir -p .data
go build -o .data/localbackup-dev ./cmd/localbackup
./.data/localbackup-dev -data "$PWD/.data" -addr 127.0.0.1:18787 -dev -web apps/web/dist &
backend_pid=$!
trap 'kill "$backend_pid" 2>/dev/null || true' EXIT INT TERM
npm run dev --prefix apps/web
