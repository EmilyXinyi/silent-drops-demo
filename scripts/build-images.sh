#!/usr/bin/env bash
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
docker build -t silent-drops-backend:dev "$ROOT/apps/backend"
docker build -t silent-drops-frontend:dev "$ROOT/apps/frontend"
docker build -t silent-drops-controller:dev -f "$ROOT/controller/Dockerfile" "$ROOT"
kind load docker-image silent-drops-backend:dev --name silent-drops
kind load docker-image silent-drops-frontend:dev --name silent-drops
kind load docker-image silent-drops-controller:dev --name silent-drops
