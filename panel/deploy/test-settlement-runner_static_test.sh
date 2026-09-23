#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/test-checkout-atomic-00039-pg18.sh"
GO_TEST="$ROOT/internal/domain/billing/settlement_pg18_test.go"
WRAPPER="$ROOT/deploy/test-settlement-00040-pg18.sh"

command -v awk >/dev/null 2>&1
command -v grep >/dev/null 2>&1

[[ -f "$RUNNER" && -f "$GO_TEST" && -f "$WRAPPER" ]]

for required in \
  'PG_CONTAINER_ID="$(docker inspect' \
  'GO_CONTAINER_ID="$(docker inspect' \
  'NETWORK_ID="$(docker network inspect' \
  'PG_VOLUME="$(docker inspect' \
  'MOD_VOLUME="$(docker inspect' \
  'cleanup_identity_mismatch=container:' \
  'cleanup_identity_mismatch=network:' \
  '--mount "type=volume,dst=/var/lib/postgresql"' \
  '--mount "type=volume,dst=/go/pkg/mod"' \
  '00040_order_release_and_late_suspense.sql' \
  'AEGIS_SETTLEMENT_PG18_DSN=' \
  'AEGIS_SETTLEMENT_PG18_EXPECT_RUN_ID=' \
  'AEGIS_SETTLEMENT_PG18_EXPECT_DB_NAME=' \
  'timeout --signal=TERM --kill-after=15s 210s docker start -a' \
  'settlement_pg18_target_identity_ok' \
  'settlement_pg18_terminal_quarantine_exactly_once_ok' \
  'BUSINESS_GATE=1'; do
  grep -Fq -- "$required" "$RUNNER"
done

if grep -Fq 'docker rm -fv "$GO_CONTAINER"' "$RUNNER" ||
   grep -Fq 'docker rm -fv "$PG_CONTAINER"' "$RUNNER" ||
   grep -Fq 'docker network rm "$NETWORK"' "$RUNNER" ||
   grep -Fq 'docker volume rm "$PG_VOLUME"' "$RUNNER" ||
   grep -Fq 'docker volume rm "$MOD_VOLUME"' "$RUNNER"; then
  echo 'runner deletes by mutable resource name' >&2
  exit 1
fi

if grep -Fq 'docker volume create' "$RUNNER" || grep -Fq 'docker volume rm' "$RUNNER"; then
  echo 'runner must use container-owned anonymous volumes only' >&2
  exit 1
fi

for forbidden in \
  'docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER"' \
  'docker start "$PG_CONTAINER"' \
  'docker exec "$PG_CONTAINER" pg_isready' \
  'docker start -a "$GO_CONTAINER"'; do
  if grep -Fq "$forbidden" "$RUNNER"; then
    echo "runner executes by mutable container name: $forbidden" >&2
    exit 1
  fi
done

[[ "$(grep -Fc 'docker rm -fv "$expected_id"' "$RUNNER")" -eq 1 ]]
grep -Fq 'grep -E '\''marker=(checkout|settlement)_pg18_'\'' "$LOG" >&3 || cleanup_rc=1' "$RUNNER"
grep -Fq 'secret_scan=error' "$RUNNER"
grep -Fq 'rm -f "${cleanup_files[@]}" || cleanup_rc=1' "$RUNNER"
grep -Fq '[[ ! -e "$cleanup_file" && ! -L "$cleanup_file" ]] || cleanup_rc=1' "$RUNNER"

[[ "$(grep -Fc 'settlement_pg18_full_suite=ok business=ok cleanup=ok schema=40' "$RUNNER")" -eq 1 ]]
final_line="$(grep -Fn 'settlement_pg18_full_suite=ok business=ok cleanup=ok schema=40' "$RUNNER" | cut -d: -f1)"
cleanup_line="$(grep -Fn 'if [[ "$main_rc" -eq 0 && "$cleanup_rc" -eq 0 && "$BUSINESS_GATE" -eq 1 ]]' "$RUNNER" | cut -d: -f1)"
business_line="$(grep -Fn 'BUSINESS_GATE=1' "$RUNNER" | tail -n1 | cut -d: -f1)"
[[ -n "$final_line" && -n "$cleanup_line" && -n "$business_line" ]]
(( final_line > cleanup_line ))
(( business_line > final_line ))

grep -Fq 'dsn := os.Getenv("AEGIS_SETTLEMENT_PG18_DSN")' "$GO_TEST"
grep -Fq 'to_regclass('\''app.order_release_00040_meta'\'') IS NOT NULL' "$GO_TEST"
grep -Fq 'orderReleasePG18AssertQuarantine(' "$GO_TEST"
grep -Fq 'settlementPG18ReleasedGraphFingerprint(' "$GO_TEST"
[[ "$(grep -Fc 'provider-payment replay mutated released resources' "$GO_TEST")" -eq 1 ]]
grep -Fq 'marker=settlement_pg18_terminal_quarantine_exactly_once_ok' "$GO_TEST"
grep -Fq 'timeout --signal=TERM --kill-after=30s 900s' "$WRAPPER"
grep -Fq 'bash "$ROOT/deploy/test-checkout-atomic-00039-pg18.sh"' "$WRAPPER"
if grep -Fq 'marker=settlement_pg18_terminal_fail_closed_ok' "$GO_TEST"; then
  echo 'stale schema-39 terminal expectation remains' >&2
  exit 1
fi

echo 'settlement_runner_static_suite=ok schema=40 cleanup_gated=yes'
