#!/bin/sh
set -eu
cd "$(dirname "$0")/../.."
# Build both production targets from this checkout. prepare.py pins their IDs.
docker build --target nanoflared -t nanoflare-pilot/daemon:local .
docker build --target nanoflare-runner -t nanoflare-pilot/runner:local .
