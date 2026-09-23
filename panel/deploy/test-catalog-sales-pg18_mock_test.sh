#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/test-catalog-sales-pg18.sh"
GO_TEST="$ROOT/internal/domain/adminops/catalog_sales_pg18_test.go"

for file in "$RUNNER" "$GO_TEST"; do
  [[ -f "$file" ]] || { printf 'catalog_sales_pg18_mock_missing=%s\n' "$file" >&2; exit 1; }
done
bash -n "$RUNNER"

digest_zero="$(printf '0%.0s' {1..64})"
selftest_output="$(
  PANDORA_TEST_POSTGRES_IMAGE="postgres@sha256:$digest_zero" \
  PANDORA_TEST_GO_IMAGE="golang@sha256:$digest_zero" \
  PANDORA_CATALOG_SALES_SELF_TEST=1 \
  bash "$RUNNER"
)"
grep -Fq 'catalog_sales_pg18_runner_selftest=PASS fake_docker=true' <<<"$selftest_output"

require_fixed() {
  local needle="$1" file="$2"
  grep -Fq -- "$needle" "$file" || { printf 'catalog_sales_pg18_mock_missing_contract=%s\n' "$needle" >&2; exit 1; }
}

for env_name in \
  AEGIS_CATALOG_SALES_PG18_FIXTURE AEGIS_CATALOG_SALES_PG18_DSN \
  AEGIS_CATALOG_SALES_PG18_ADMIN_DSN AEGIS_CATALOG_SALES_PG18_DATABASE \
  AEGIS_CATALOG_SALES_PG18_RUN_ID; do
  require_fixed "$env_name" "$GO_TEST"
  require_fixed "$env_name" "$RUNNER"
done

for contract in \
  'TestUpdatePlanP0BSalesGateOrderPG18' \
  'platformdb.Open' \
  'currentRole != "aegis_app"' \
  'relrowsecurity AND relforcerowsecurity' \
  'platformdb.Scope{TenantID: tenantID' \
  'httpx.CodeUnavailable' \
  'httpx.CodeConflict' \
  'afterDenied != before' \
  'afterConflict != before' \
  'conflict.Fields["row_version"] != "current=7"' \
  'catalog_sales_pg18_business=ok'; do
  require_fixed "$contract" "$GO_TEST"
done

for contract in \
  'set PANDORA_TEST_POSTGRES_IMAGE to a locally present postgres@sha256 digest' \
  'set PANDORA_TEST_GO_IMAGE to a locally present golang@sha256 digest' \
  'docker image inspect' \
  '/proc/sys/kernel/random/uuid' \
  'pandora.test=catalog-sales-pg18' \
  'verify_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME"' \
  'verify_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME"' \
  "'{{json .HostConfig.PortBindings}}'" \
  '"$ROOT:/src:ro"' \
  'for n in $(seq 1 41)' \
  "version_id BETWEEN 1 AND 41" \
  'version_id > 41' \
  'pandora_catalog_sales_test_marker' \
  'pg_control_system()' \
  'pandora-catalog-sales-pg18:$RUN_ID' \
  'remove_owned_container' \
  'remove_owned_network' \
  'remove_owned_volume' \
  'PANDORA_CATALOG_SALES_SELF_TEST' \
  'catalog_sales_pg18_runner_selftest=PASS fake_docker=true' \
  'catalog_sales_pg18_full_suite=ok cleanup=ok business=ok schema=41'; do
  require_fixed "$contract" "$RUNNER"
done

if grep -Fq 'docker pull' "$RUNNER"; then
  echo 'catalog_sales_pg18_mock_refused_implicit_image_pull' >&2
  exit 1
fi
if grep -Eq '00042_|00043_' "$RUNNER"; then
  echo 'catalog_sales_pg18_mock_refused_post_schema41_migration' >&2
  exit 1
fi
[[ "$(grep -Fc "echo 'catalog_sales_pg18_full_suite=ok cleanup=ok business=ok schema=41'" "$RUNNER")" -eq 1 ]]

echo 'catalog_sales_pg18_mock=PASS static_only=true real_pg18=NOT_RUN'
