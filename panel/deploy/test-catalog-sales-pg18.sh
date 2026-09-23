#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:?set PANDORA_TEST_POSTGRES_IMAGE to a locally present postgres@sha256 digest}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:?set PANDORA_TEST_GO_IMAGE to a locally present golang@sha256 digest}"
[[ "$POSTGRES_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'catalog_sales_pg18_refused_mutable_postgres_image' >&2; exit 2; }
[[ "$GO_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'catalog_sales_pg18_refused_mutable_go_image' >&2; exit 2; }

if [[ "${PANDORA_CATALOG_SALES_SELF_TEST:-0}" == 1 ]]; then
  RUN_ID="00000000000000000000000000000000"
else
  RUN_ID="$(tr -d '-' </proc/sys/kernel/random/uuid)"
fi
[[ "$RUN_ID" =~ ^[0-9a-f]{32}$ ]] || { echo 'catalog_sales_pg18_random_identity_failed' >&2; exit 2; }
PG_CONTAINER="pandora-catalog-sales-pg18-${RUN_ID}"
GO_CONTAINER="pandora-catalog-sales-go-${RUN_ID}"
NETWORK="pandora-catalog-sales-net-${RUN_ID}"
PG_VOLUME="pandora-catalog-sales-pgdata-${RUN_ID}"
MOD_VOLUME="pandora-catalog-sales-gomod-${RUN_ID}"
DB_NAME="pandora_catalog_sales_${RUN_ID}"
POSTGRES_PASSWORD="catalog-sales-pg18-test-only"
APP_PASSWORD="catalog-sales-aegis-app-test-only-2026"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
ADMIN_DSN="postgres://postgres:${POSTGRES_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER_ID=""
GO_CONTAINER_ID=""
NETWORK_ID=""
PG_VOLUME_ID=""
MOD_VOLUME_ID=""
BUSINESS_OK=0

inspect_absent() {
  local kind="$1" name="$2"
  if docker "$kind" inspect "$name" >/dev/null 2>&1; then
    printf 'catalog_sales_pg18_refused_preexisting_%s=%s\n' "$kind" "$name" >&2
    return 1
  fi
  docker info >/dev/null 2>&1 || { printf 'catalog_sales_pg18_%s_inspect_infrastructure_error=%s\n' "$kind" "$name" >&2; return 1; }
  docker "$kind" inspect "$name" >/dev/null 2>&1 && { printf 'catalog_sales_pg18_%s_inspect_transient_error=%s\n' "$kind" "$name" >&2; return 1; }
	return 0
}

emit_final_markers() {
  local rc="$1" cleanup_rc="$2" business_ok="$3"
  printf 'catalog_sales_pg18_cleanup=%s\n' "$([[ "$cleanup_rc" -eq 0 ]] && echo ok || echo failed)"
  if [[ "$rc" -eq 0 && "$cleanup_rc" -eq 0 && "$business_ok" -eq 1 ]]; then
    echo 'catalog_sales_pg18_full_suite=ok cleanup=ok business=ok schema=41'
  fi
}

if [[ "${PANDORA_CATALOG_SALES_SELF_TEST:-0}" == 1 ]]; then
  SELF_DOCKER_MODE=absent
  docker() {
    if [[ "$1" == info ]]; then
      [[ "$SELF_DOCKER_MODE" != daemon_error ]]
      return
    fi
    if [[ "$2" == inspect ]]; then
      [[ "$SELF_DOCKER_MODE" == existing ]]
      return
    fi
    return 97
  }
  inspect_absent container selftest-absent >/dev/null 2>&1 || { echo 'catalog_sales_pg18_selftest_absent_failed' >&2; exit 1; }
  SELF_DOCKER_MODE=existing
  if inspect_absent container selftest-existing >/dev/null 2>&1; then
    echo 'catalog_sales_pg18_selftest_existing_failed' >&2
    exit 1
  fi
  SELF_DOCKER_MODE=daemon_error
  if inspect_absent container selftest-daemon >/dev/null 2>&1; then
    echo 'catalog_sales_pg18_selftest_daemon_failed' >&2
    exit 1
  fi
  success_markers="$(emit_final_markers 0 0 1)"
  [[ "$(grep -Fc 'catalog_sales_pg18_full_suite=ok cleanup=ok business=ok schema=41' <<<"$success_markers")" -eq 1 ]]
  if emit_final_markers 0 1 1 | grep -Fq 'catalog_sales_pg18_full_suite='; then
    echo 'catalog_sales_pg18_selftest_cleanup_failure_false_pass' >&2
    exit 1
  fi
  if emit_final_markers 0 0 0 | grep -Fq 'catalog_sales_pg18_full_suite='; then
    echo 'catalog_sales_pg18_selftest_business_failure_false_pass' >&2
    exit 1
  fi
  echo 'catalog_sales_pg18_runner_selftest=PASS fake_docker=true'
  exit 0
fi

for command_name in docker awk grep seq tr; do
  command -v "$command_name" >/dev/null 2>&1 || { printf 'catalog_sales_pg18_blocked_command=%s\n' "$command_name" >&2; exit 2; }
done
docker info >/dev/null 2>&1 || { echo 'catalog_sales_pg18_blocked_docker_daemon' >&2; exit 2; }

for name in "$PG_CONTAINER" "$GO_CONTAINER"; do inspect_absent container "$name"; done
inspect_absent network "$NETWORK"
for name in "$PG_VOLUME" "$MOD_VOLUME"; do inspect_absent volume "$name"; done

remove_owned_container() {
  local id="$1" expected="$2" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker container inspect -f '{{.Id}}|{{.Name}}|{{index .Config.Labels "pandora.test"}}|{{index .Config.Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || { printf 'catalog_sales_pg18_container_inspect_infrastructure_error=%s\n' "$expected" >&2; return 1; }
    docker container inspect "$id" >/dev/null 2>&1 && { printf 'catalog_sales_pg18_container_inspect_transient_error=%s\n' "$expected" >&2; return 1; }
    return 0
  fi
  [[ "$actual" == "$id|/$expected|catalog-sales-pg18|$RUN_ID" ]] || {
    printf 'catalog_sales_pg18_refused_unowned_container=%s actual=%s\n' "$expected" "$actual" >&2
    return 1
  }
  docker rm -fv "$id" >/dev/null
}

remove_owned_network() {
  local id="$1" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker network inspect -f '{{.Id}}|{{.Name}}|{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || { printf 'catalog_sales_pg18_network_inspect_infrastructure_error=%s\n' "$NETWORK" >&2; return 1; }
    docker network inspect "$id" >/dev/null 2>&1 && { printf 'catalog_sales_pg18_network_inspect_transient_error=%s\n' "$NETWORK" >&2; return 1; }
    return 0
  fi
  [[ "$actual" == "$id|$NETWORK|catalog-sales-pg18|$RUN_ID" ]] || {
    printf 'catalog_sales_pg18_refused_unowned_network=%s\n' "$actual" >&2
    return 1
  }
  docker network rm "$id" >/dev/null
}

verify_owned_volume() {
  local identity="$1" name="$2" actual labels
  actual="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$name" 2>/dev/null)" || return 1
  labels="$(docker volume inspect -f '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$name")" || return 1
  [[ "$actual" == "$identity" && "$labels" == "catalog-sales-pg18|$RUN_ID" ]] || {
    printf 'catalog_sales_pg18_refused_unowned_volume=%s actual=%s labels=%s\n' "$name" "$actual" "$labels" >&2
    return 1
  }
}

remove_owned_volume() {
  local identity="$1" name="$2"
  [[ -n "$identity" ]] || return 0
  if ! docker volume inspect "$name" >/dev/null 2>&1; then
    docker info >/dev/null 2>&1 || { printf 'catalog_sales_pg18_volume_inspect_infrastructure_error=%s\n' "$name" >&2; return 1; }
    docker volume inspect "$name" >/dev/null 2>&1 && { printf 'catalog_sales_pg18_volume_inspect_transient_error=%s\n' "$name" >&2; return 1; }
    return 0
  fi
  verify_owned_volume "$identity" "$name" || return 1
  docker volume rm "$name" >/dev/null
}

cleanup() {
  local rc="$1" cleanup_rc=0
  trap - EXIT INT TERM
  set +e
  remove_owned_container "$GO_CONTAINER_ID" "$GO_CONTAINER" || cleanup_rc=1
  remove_owned_container "$PG_CONTAINER_ID" "$PG_CONTAINER" || cleanup_rc=1
  remove_owned_network "$NETWORK_ID" || cleanup_rc=1
  remove_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME" || cleanup_rc=1
  remove_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME" || cleanup_rc=1
  for name in "$PG_CONTAINER" "$GO_CONTAINER"; do inspect_absent container "$name" || cleanup_rc=1; done
  inspect_absent network "$NETWORK" || cleanup_rc=1
  for name in "$PG_VOLUME" "$MOD_VOLUME"; do inspect_absent volume "$name" || cleanup_rc=1; done
  [[ "$cleanup_rc" -eq 0 ]] || rc=1
  emit_final_markers "$rc" "$cleanup_rc" "$BUSINESS_OK"
  exit "$rc"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1; next} up' "$1"; }
psql_admin() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER_ID" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" "$@"
}

PG_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")" || { echo 'catalog_sales_pg18_postgres_image_not_local' >&2; exit 2; }
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")" || { echo 'catalog_sales_pg18_go_image_not_local' >&2; exit 2; }
[[ "$PG_IMAGE_ID" == sha256:* && "$GO_IMAGE_ID" == sha256:* ]]

NETWORK_ID="$(docker network create --label pandora.test=catalog-sales-pg18 --label "pandora.run=$RUN_ID" "$NETWORK")"
docker volume create --label pandora.test=catalog-sales-pg18 --label "pandora.run=$RUN_ID" "$PG_VOLUME" >/dev/null
PG_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$PG_VOLUME")"
verify_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME"
docker volume create --label pandora.test=catalog-sales-pg18 --label "pandora.run=$RUN_ID" "$MOD_VOLUME" >/dev/null
MOD_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$MOD_VOLUME")"
verify_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME"

PG_CONTAINER_ID="$(docker create --name "$PG_CONTAINER" --label pandora.test=catalog-sales-pg18 --label "pandora.run=$RUN_ID" \
  --network "$NETWORK_ID" --network-alias pg18 -v "$PG_VOLUME:/var/lib/postgresql" \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" -e POSTGRES_DB="$DB_NAME" "$PG_IMAGE_ID")"
docker start "$PG_CONTAINER_ID" >/dev/null
for _ in $(seq 1 90); do
  docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER_ID")" =~ ^(\{\}|null)$ ]]
[[ "$(docker exec "$PG_CONTAINER_ID" psql -XAt -U postgres -d "$DB_NAME" -c 'SHOW server_version_num')" == 18* ]]

psql_admin >/dev/null <<'SQL'
CREATE TABLE public.goose_db_version (
  id serial PRIMARY KEY,
  version_id bigint NOT NULL,
  is_applied boolean NOT NULL,
  tstamp timestamp NOT NULL DEFAULT now()
);
INSERT INTO public.goose_db_version(version_id,is_applied) VALUES (0,true);
SQL

migration_count=0
last_migration=""
for n in $(seq 1 41); do
  number="$(printf '%05d' "$n")"
  shopt -s nullglob
  matches=("$ROOT"/migrations/"$number"_*.sql)
  shopt -u nullglob
  [[ "${#matches[@]}" -eq 1 ]] || { printf 'catalog_sales_pg18_migration_match_%s=%d\n' "$number" "${#matches[@]}" >&2; exit 1; }
  migration="${matches[0]}"
  {
    echo 'BEGIN;'
    up_sql "$migration"
    printf 'INSERT INTO public.goose_db_version(version_id,is_applied) VALUES (%d,true);\n' "$n"
    echo 'COMMIT;'
  } | psql_admin >/dev/null
  migration_count=$((migration_count + 1))
  last_migration="$(basename "$migration")"
done
[[ "$migration_count" -eq 41 && "$last_migration" == 00041_* ]]
[[ "$(psql_admin -Atc "SELECT count(*) FILTER (WHERE is_applied AND version_id BETWEEN 1 AND 41), max(version_id), count(*) FILTER (WHERE version_id > 41) FROM goose_db_version")" == '41|41|0' ]]

docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
  "$PG_CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null

psql_admin -v run_id="$RUN_ID" -v db_comment="pandora-catalog-sales-pg18:$RUN_ID" >/dev/null <<'SQL'
CREATE TABLE public.pandora_catalog_sales_test_marker (
  run_id text PRIMARY KEY,
  system_identifier text NOT NULL,
  database_oid oid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON public.pandora_catalog_sales_test_marker FROM PUBLIC, aegis_app;
INSERT INTO public.pandora_catalog_sales_test_marker(run_id,system_identifier,database_oid)
SELECT :'run_id', pcs.system_identifier::text, d.oid
  FROM pg_control_system() AS pcs
  JOIN pg_database AS d ON d.datname=current_database();
GRANT SELECT ON public.pandora_catalog_sales_test_marker TO aegis_app;
SELECT format('COMMENT ON DATABASE %I IS %L', current_database(), :'db_comment') \gexec
SQL

GO_CONTAINER_ID="$(docker create --name "$GO_CONTAINER" --label pandora.test=catalog-sales-pg18 --label "pandora.run=$RUN_ID" \
  --network "$NETWORK_ID" -v "$ROOT:/src:ro" -v "$MOD_VOLUME:/go/pkg/mod" -w /src \
  -e AEGIS_CATALOG_SALES_PG18_FIXTURE=disposable-v1 \
  -e "AEGIS_CATALOG_SALES_PG18_DSN=$APP_DSN" -e "AEGIS_CATALOG_SALES_PG18_ADMIN_DSN=$ADMIN_DSN" \
  -e "AEGIS_CATALOG_SALES_PG18_DATABASE=$DB_NAME" -e "AEGIS_CATALOG_SALES_PG18_RUN_ID=$RUN_ID" \
  "$GO_IMAGE_ID" sh -ec 'go test -mod=readonly -buildvcs=false -v -count=1 -timeout=120s -run "^TestUpdatePlanP0BSalesGateOrderPG18$" ./internal/domain/adminops')"
set +e
GO_OUTPUT="$(docker start -a "$GO_CONTAINER_ID" 2>&1)"
GO_RC=$?
set -e
printf '%s\n' "$GO_OUTPUT"
[[ "$GO_RC" -eq 0 ]]
grep -Fq 'catalog_sales_pg18_business=ok unavailable_before_stale=true denied_writes=0 denied_audits=0 allowed_stale=conflict role=aegis_app rls=on schema=41' <<<"$GO_OUTPUT"
BUSINESS_OK=1

printf 'catalog_sales_pg18_images=%s,%s\n' "$PG_IMAGE_ID" "$GO_IMAGE_ID"
printf 'catalog_sales_pg18_real_migrations=%d last=%s\n' "$migration_count" "$last_migration"
