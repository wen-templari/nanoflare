#!/bin/sh
set -eu
cd "$(dirname "$0")"
exec docker compose --project-directory "$PWD" --env-file private/pilot.env -p nanoflare-pilot -f private/compose.lock.json "$@"
