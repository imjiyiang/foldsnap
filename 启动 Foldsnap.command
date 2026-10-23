#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")"
if [ ! -x bin/localbackup ]; then make build; fi
exec ./bin/localbackup -data "$PWD/.data" -addr 127.0.0.1:18787 -open
