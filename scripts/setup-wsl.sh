#!/usr/bin/env bash
# Install the native Ubuntu/WSL development dependencies and local databases.
set -euo pipefail

project_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$project_dir"

if (( EUID == 0 )); then
  printf 'Run this script as your normal WSL user; it invokes sudo where needed.\n' >&2
  exit 1
fi
if ! command -v apt-get >/dev/null || ! command -v go >/dev/null; then
  printf 'This setup requires Ubuntu/Debian with Go 1.23+ installed.\n' >&2
  exit 1
fi
if [[ ! -e .env ]]; then
  install -m 600 .env.example .env
fi

sudo -v
sudo apt-get update
sudo apt-get install -y postgresql postgresql-client build-essential ca-certificates
if [[ "$(ps -p 1 -o comm=)" == systemd ]]; then
  sudo systemctl enable --now postgresql
else
  sudo service postgresql start
fi

# Only create missing objects; keep existing role passwords and databases.
sudo -u postgres psql --no-psqlrc --set ON_ERROR_STOP=on postgres <<'SQL'
SELECT 'CREATE ROLE workflow LOGIN PASSWORD ''workflow-local'''
WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workflow')
\gexec
SELECT 'CREATE DATABASE workflow OWNER workflow'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'workflow')
\gexec
SELECT 'CREATE DATABASE workflow_test OWNER workflow'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'workflow_test')
\gexec
SQL

PGPASSWORD=workflow-local psql --no-psqlrc -h 127.0.0.1 -U workflow -d workflow -v ON_ERROR_STOP=on -Atc 'SELECT current_database()' >/dev/null
PGPASSWORD=workflow-local psql --no-psqlrc -h 127.0.0.1 -U workflow -d workflow_test -v ON_ERROR_STOP=on -Atc 'SELECT current_database()' >/dev/null
printf '\nNative PostgreSQL and the C compiler are ready. Existing .env files were preserved.\n'
printf 'Run: make run\nTests: make race && make integration-local\n'
