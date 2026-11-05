#!/usr/bin/env bash
# Load the project's trusted shell-format .env before running a command.
set -euo pipefail
project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"
if [[ -f .env ]]; then
  set -a
  source ./.env
  set +a
fi
exec "$@"
