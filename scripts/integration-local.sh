#!/usr/bin/env bash
set -euo pipefail
# with-env.sh has already loaded the local configuration.
: "${TEST_DATABASE_URL:?Set TEST_DATABASE_URL in .env or your shell to a local PostgreSQL database}"
CGO_ENABLED=1 exec go test -race -tags integration -count=1 ./...
