#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
command -v timeout >/dev/null 2>&1 || { echo 'timeout is required' >&2; exit 1; }

# Settlement depends on the schema-39 atomic checkout contract, so the
# canonical schema-40 gate intentionally runs both tests in one disposable
# PostgreSQL 18 environment and emits a cleanup-gated terminal marker only
# after every owned Docker resource is gone.
exec timeout --signal=TERM --kill-after=30s 900s \
  bash "$ROOT/deploy/test-checkout-atomic-00039-pg18.sh" "$@"
