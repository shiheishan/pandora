#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
RUN_ID="$(date -u +%Y%m%dt%H%M%sz)-$$-${RANDOM}"
PG_CONTAINER="pandora-logout-pg18-${RUN_ID}"
GO_CONTAINER="pandora-logout-go-${RUN_ID}"
NETWORK="pandora-logout-net-${RUN_ID}"
PG_VOLUME="pandora-logout-pgdata-${RUN_ID}"
MOD_VOLUME="pandora-logout-gomod-${RUN_ID}"
DB_NAME="pandora_logout_${RUN_ID//-/_}"
POSTGRES_PASSWORD="logout-pg18-test-only"
APP_PASSWORD="logout-aegis-app-test-only-2026"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
ADMIN_DSN="postgres://postgres:${POSTGRES_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
PG_CONTAINER_ID=""
GO_CONTAINER_ID=""
NETWORK_ID=""
PG_VOLUME_ID=""
MOD_VOLUME_ID=""

command -v docker >/dev/null 2>&1 || { echo 'logout_pg18_blocked_docker_missing' >&2; exit 2; }
docker info >/dev/null 2>&1 || { echo 'logout_pg18_blocked_docker_daemon' >&2; exit 2; }

for name in "$PG_CONTAINER" "$GO_CONTAINER"; do
  if docker container inspect "$name" >/dev/null 2>&1; then
    printf 'logout_pg18_refused_preexisting_container=%s\n' "$name" >&2
    exit 1
  fi
done
if docker network inspect "$NETWORK" >/dev/null 2>&1; then
  printf 'logout_pg18_refused_preexisting_network=%s\n' "$NETWORK" >&2
  exit 1
fi
for name in "$PG_VOLUME" "$MOD_VOLUME"; do
  if docker volume inspect "$name" >/dev/null 2>&1; then
    printf 'logout_pg18_refused_preexisting_volume=%s\n' "$name" >&2
    exit 1
  fi
done

remove_owned_container() {
  local id="$1" expected="$2" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker container inspect -f '{{.Id}}|{{.Name}}|{{index .Config.Labels "pandora.test"}}|{{index .Config.Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || { printf 'logout_pg18_container_inspect_infrastructure_error=%s\n' "$expected" >&2; return 1; }
    docker container inspect "$id" >/dev/null 2>&1 && { printf 'logout_pg18_container_inspect_transient_error=%s\n' "$expected" >&2; return 1; }
    return 0
  fi
  [[ "$actual" == "$id|/$expected|logout-pg18|$RUN_ID" ]] || {
    printf 'logout_pg18_refused_unowned_container=%s actual=%s\n' "$expected" "$actual" >&2
    return 1
  }
  docker rm -fv "$id" >/dev/null
}

remove_owned_network() {
  local id="$1" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker network inspect -f '{{.Id}}|{{.Name}}|{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || { printf 'logout_pg18_network_inspect_infrastructure_error=%s\n' "$NETWORK" >&2; return 1; }
    docker network inspect "$id" >/dev/null 2>&1 && { printf 'logout_pg18_network_inspect_transient_error=%s\n' "$NETWORK" >&2; return 1; }
    return 0
  fi
  [[ "$actual" == "$id|$NETWORK|logout-pg18|$RUN_ID" ]] || {
    printf 'logout_pg18_refused_unowned_network=%s\n' "$actual" >&2
    return 1
  }
  docker network rm "$id" >/dev/null
}

verify_owned_volume() {
  local identity="$1" name="$2" actual labels
  [[ -n "$identity" ]] || return 0
  actual="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$name" 2>/dev/null)" || return 1
  labels="$(docker volume inspect -f '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$name")" || return 1
  [[ "$actual" == "$identity" && "$labels" == "logout-pg18|$RUN_ID" ]] || {
    printf 'logout_pg18_refused_unowned_volume=%s actual=%s labels=%s\n' "$name" "$actual" "$labels" >&2
    return 1
  }
}

remove_owned_volume() {
  local identity="$1" name="$2"
  [[ -n "$identity" ]] || return 0
  if ! docker volume inspect "$name" >/dev/null 2>&1; then
    docker info >/dev/null 2>&1 || { printf 'logout_pg18_volume_inspect_infrastructure_error=%s\n' "$name" >&2; return 1; }
    docker volume inspect "$name" >/dev/null 2>&1 && { printf 'logout_pg18_volume_inspect_transient_error=%s\n' "$name" >&2; return 1; }
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
  [[ "$cleanup_rc" -eq 0 ]] || rc=1
  printf 'logout_pg18_cleanup=%s\n' "$([[ "$cleanup_rc" -eq 0 ]] && echo ok || echo failed)"
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

docker pull "$POSTGRES_IMAGE" >/dev/null
docker pull "$GO_IMAGE" >/dev/null
PG_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")"
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")"
[[ "$PG_IMAGE_ID" == sha256:* && "$GO_IMAGE_ID" == sha256:* ]]

NETWORK_ID="$(docker network create --label pandora.test=logout-pg18 --label "pandora.run=$RUN_ID" "$NETWORK")"
docker volume create --label pandora.test=logout-pg18 --label "pandora.run=$RUN_ID" "$PG_VOLUME" >/dev/null
PG_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$PG_VOLUME")"
verify_owned_volume "$PG_VOLUME_ID" "$PG_VOLUME"
docker volume create --label pandora.test=logout-pg18 --label "pandora.run=$RUN_ID" "$MOD_VOLUME" >/dev/null
MOD_VOLUME_ID="$(docker volume inspect -f '{{.Name}}|{{.Mountpoint}}' "$MOD_VOLUME")"
verify_owned_volume "$MOD_VOLUME_ID" "$MOD_VOLUME"

PG_CONTAINER_ID="$(docker create --name "$PG_CONTAINER" --label pandora.test=logout-pg18 --label "pandora.run=$RUN_ID" \
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

migration_count=0
last_migration=""
for migration in "$ROOT"/migrations/*.sql; do
  up_sql "$migration" | psql_admin >/dev/null
  migration_count=$((migration_count + 1))
  last_migration="$(basename "$migration")"
done
expected_migration_count="$(printf '%s\n' "$ROOT"/migrations/*.sql | wc -l | tr -d ' ')"
expected_last_migration="$(printf '%s\n' "$ROOT"/migrations/*.sql | sort | tail -n1)"
expected_last_migration="${expected_last_migration##*/}"
[[ "$migration_count" -eq "$expected_migration_count" &&
   "$last_migration" == "$expected_last_migration" ]]

docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
  "$PG_CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null

psql_admin -v run_id="$RUN_ID" -v db_comment="pandora-logout-pg18:$RUN_ID" >/dev/null <<'SQL'
CREATE TABLE public.pandora_logout_test_marker (
  run_id text PRIMARY KEY,
  system_identifier text NOT NULL,
  database_oid oid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON public.pandora_logout_test_marker FROM PUBLIC, aegis_app;
INSERT INTO public.pandora_logout_test_marker(run_id,system_identifier,database_oid)
SELECT :'run_id', pcs.system_identifier::text, d.oid
  FROM pg_control_system() AS pcs
  JOIN pg_database AS d ON d.datname=current_database();
GRANT SELECT ON public.pandora_logout_test_marker TO aegis_app;
SELECT format('COMMENT ON DATABASE %I IS %L', current_database(), :'db_comment') \gexec
SQL

GO_CONTAINER_ID="$(docker create --name "$GO_CONTAINER" --label pandora.test=logout-pg18 --label "pandora.run=$RUN_ID" \
  --network "$NETWORK_ID" -v "$ROOT:/src:ro" -v "$MOD_VOLUME:/go/pkg/mod" -w /src \
  -e AEGIS_LOGOUT_PG18_FIXTURE=disposable-v1 \
  -e "AEGIS_LOGOUT_PG18_DSN=$APP_DSN" -e "AEGIS_LOGOUT_PG18_ADMIN_DSN=$ADMIN_DSN" \
  -e "AEGIS_LOGOUT_PG18_DATABASE=$DB_NAME" -e "AEGIS_LOGOUT_PG18_RUN_ID=$RUN_ID" \
  "$GO_IMAGE_ID" sh -ec 'go test -buildvcs=false -v -count=1 -timeout=120s -run "^TestLogoutCurrentSessionPG18ConcurrentSingleTransition$" ./internal/domain/identity')"
docker start -a "$GO_CONTAINER_ID"

printf 'logout_pg18_images=%s,%s\n' "$PG_IMAGE_ID" "$GO_IMAGE_ID"
printf 'logout_pg18_real_migrations=%d last=%s\n' "$migration_count" "$last_migration"
printf '%s\n' logout_pg18_concurrent_single_transition_old_token_401_ok
