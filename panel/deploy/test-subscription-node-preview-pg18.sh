#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:?set PANDORA_TEST_POSTGRES_IMAGE to a locally present postgres@sha256 digest}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:?set PANDORA_TEST_GO_IMAGE to a locally present golang@sha256 digest}"
[[ "$POSTGRES_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'node_preview_pg18_refused_mutable_postgres_image' >&2; exit 2; }
[[ "$GO_IMAGE" =~ ^.+@sha256:[0-9a-f]{64}$ ]] || { echo 'node_preview_pg18_refused_mutable_go_image' >&2; exit 2; }

for command_name in docker awk grep seq tr head od; do
  command -v "$command_name" >/dev/null 2>&1 || { printf 'node_preview_pg18_blocked_command=%s\n' "$command_name" >&2; exit 2; }
done
docker info >/dev/null 2>&1 || { echo 'node_preview_pg18_blocked_docker_daemon' >&2; exit 2; }

RUN_ID="$(tr -d '-' </proc/sys/kernel/random/uuid)"
[[ "$RUN_ID" =~ ^[0-9a-f]{32}$ ]] || { echo 'node_preview_pg18_random_identity_failed' >&2; exit 2; }
PG_CONTAINER="pandora-node-preview-pg18-${RUN_ID}"
GO_CONTAINER="pandora-node-preview-go-${RUN_ID}"
NETWORK="pandora-node-preview-net-${RUN_ID}"
DB_NAME="pandora_node_preview_${RUN_ID}"
POSTGRES_PASSWORD="node-preview-pg18-test-only"
APP_PASSWORD="node-preview-aegis-app-test-only-2026"
APP_DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
ADMIN_DSN="postgres://postgres:${POSTGRES_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
PREBUILT_MODE="${PANDORA_PG18_PREBUILT_MODE:-}"
PG_CPUS="${PANDORA_PG18_POSTGRES_CPUS:-0.75}"
PG_MEMORY="${PANDORA_PG18_POSTGRES_MEMORY:-768m}"
PG_CONTAINER_ID=""
GO_CONTAINER_ID=""
NETWORK_ID=""
BUSINESS_OK=0

inspect_absent() {
  local kind="$1" name="$2"
  if docker "$kind" inspect "$name" >/dev/null 2>&1; then
    printf 'node_preview_pg18_refused_preexisting_%s=%s\n' "$kind" "$name" >&2
    return 1
  fi
  docker info >/dev/null 2>&1 || return 1
  ! docker "$kind" inspect "$name" >/dev/null 2>&1
}

for name in "$PG_CONTAINER" "$GO_CONTAINER"; do inspect_absent container "$name"; done
inspect_absent network "$NETWORK"

remove_owned_container() {
  local id="$1" expected="$2" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker container inspect -f '{{.Id}}|{{.Name}}|{{index .Config.Labels "pandora.test"}}|{{index .Config.Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || return 1
    return 0
  fi
  [[ "$actual" == "$id|/$expected|node-preview-pg18|$RUN_ID" ]] || {
    printf 'node_preview_pg18_refused_unowned_container=%s actual=%s\n' "$expected" "$actual" >&2
    return 1
  }
  docker rm -fv "$id" >/dev/null
}

remove_owned_network() {
  local id="$1" actual
  [[ -n "$id" ]] || return 0
  if ! actual="$(docker network inspect -f '{{.Id}}|{{.Name}}|{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$id" 2>/dev/null)"; then
    docker info >/dev/null 2>&1 || return 1
    return 0
  fi
  [[ "$actual" == "$id|$NETWORK|node-preview-pg18|$RUN_ID" ]] || {
    printf 'node_preview_pg18_refused_unowned_network=%s actual=%s\n' "$NETWORK" "$actual" >&2
    return 1
  }
  docker network rm "$id" >/dev/null
}

cleanup() {
  local rc="$1" cleanup_rc=0
  trap - EXIT INT TERM
  set +e
  remove_owned_container "$GO_CONTAINER_ID" "$GO_CONTAINER" || cleanup_rc=1
  remove_owned_container "$PG_CONTAINER_ID" "$PG_CONTAINER" || cleanup_rc=1
  remove_owned_network "$NETWORK_ID" || cleanup_rc=1
  for name in "$PG_CONTAINER" "$GO_CONTAINER"; do inspect_absent container "$name" || cleanup_rc=1; done
  inspect_absent network "$NETWORK" || cleanup_rc=1
  printf 'node_preview_pg18_cleanup=%s\n' "$([[ "$cleanup_rc" -eq 0 ]] && echo ok || echo failed)"
  if [[ "$rc" -eq 0 && "$cleanup_rc" -eq 0 && "$BUSINESS_OK" -eq 1 ]]; then
    echo 'node_preview_pg18_full_suite=ok cleanup=ok business=ok schema=40'
  else
    rc=1
  fi
  exit "$rc"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1; next} up' "$1"; }
MIGRATION_PGOPTIONS='-c app.idempotency_writers_stopped=yes -c app.allow_idempotency_schema37_up=yes -c app.allow_idempotency_schema38_up=yes -c app.allow_idempotency_schema39_up=yes'
psql_admin() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e PGOPTIONS="$MIGRATION_PGOPTIONS" "$PG_CONTAINER_ID" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" "$@"
}

PG_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")" || { echo 'node_preview_pg18_postgres_image_not_local' >&2; exit 2; }
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")" || { echo 'node_preview_pg18_go_image_not_local' >&2; exit 2; }
[[ "$PG_IMAGE_ID" == sha256:* && "$GO_IMAGE_ID" == sha256:* ]]

NETWORK_ID="$(docker network create --label pandora.test=node-preview-pg18 --label "pandora.run=$RUN_ID" "$NETWORK")"
PG_CONTAINER_ID="$(docker create --name "$PG_CONTAINER" --label pandora.test=node-preview-pg18 --label "pandora.run=$RUN_ID" \
  --network "$NETWORK_ID" --network-alias pg18 --cpus "$PG_CPUS" --memory "$PG_MEMORY" \
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
for n in $(seq 1 40); do
  number="$(printf '%05d' "$n")"
  shopt -s nullglob
  matches=("$ROOT"/migrations/"$number"_*.sql)
  shopt -u nullglob
  [[ "${#matches[@]}" -eq 1 ]] || { printf 'node_preview_pg18_migration_match_%s=%d\n' "$number" "${#matches[@]}" >&2; exit 1; }
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
[[ "$migration_count" -eq 40 && "$last_migration" == 00040_* ]]

docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
  "$PG_CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null

psql_admin -v run_id="$RUN_ID" -v db_comment="pandora-node-preview-pg18:$RUN_ID" >/dev/null <<'SQL'
CREATE TABLE public.pandora_node_preview_test_marker (
  run_id text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE public.pandora_announcement_test_marker (
  run_id text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE public.pandora_support_test_marker (
  run_id text PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);
REVOKE ALL ON public.pandora_node_preview_test_marker FROM PUBLIC, aegis_app;
REVOKE ALL ON public.pandora_announcement_test_marker FROM PUBLIC, aegis_app;
REVOKE ALL ON public.pandora_support_test_marker FROM PUBLIC, aegis_app;
INSERT INTO public.pandora_node_preview_test_marker(run_id) VALUES(:'run_id');
INSERT INTO public.pandora_announcement_test_marker(run_id) VALUES(:'run_id');
INSERT INTO public.pandora_support_test_marker(run_id) VALUES(:'run_id');
GRANT SELECT ON public.pandora_node_preview_test_marker TO aegis_app;
GRANT SELECT ON public.pandora_announcement_test_marker TO aegis_app;
GRANT SELECT ON public.pandora_support_test_marker TO aegis_app;
SELECT format('COMMENT ON DATABASE %I IS %L', current_database(), :'db_comment') \gexec
SQL

GO_CPUS=1
GO_MEMORY=1536m
GO_COMMAND='go test -mod=readonly -buildvcs=false -v -count=1 -timeout=120s -run "^TestNodePreviewPG18$" ./internal/domain/subscription && go test -mod=readonly -buildvcs=false -v -count=1 -timeout=120s -run "^TestAnnouncementPG18$" ./internal/api/admin'
if [[ "$PREBUILT_MODE" == "announcement" ]]; then
  [[ -f "$ROOT/output/pg18-bin/admin.test" ]] || { echo 'announcement_pg18_prebuilt_binary_missing' >&2; exit 2; }
  [[ "$(head -c 4 "$ROOT/output/pg18-bin/admin.test" | od -An -tx1 | tr -d ' \n')" == '7f454c46' ]] || {
    echo 'announcement_pg18_prebuilt_binary_not_elf' >&2; exit 2;
  }
  GO_CPUS=0.25
  GO_MEMORY=384m
  GO_COMMAND='/src/output/pg18-bin/admin.test -test.v -test.count=1 -test.timeout=120s -test.run "^TestAnnouncementPG18$"'
elif [[ "$PREBUILT_MODE" == "support" ]]; then
  [[ -f "$ROOT/output/pg18-bin/support.test" ]] || { echo 'support_pg18_prebuilt_binary_missing' >&2; exit 2; }
  [[ "$(head -c 4 "$ROOT/output/pg18-bin/support.test" | od -An -tx1 | tr -d ' \n')" == '7f454c46' ]] || {
    echo 'support_pg18_prebuilt_binary_not_elf' >&2; exit 2;
  }
  GO_CPUS=0.25
  GO_MEMORY=384m
  GO_COMMAND='/src/output/pg18-bin/support.test -test.v -test.count=1 -test.timeout=120s -test.run "^TestSupportPG18$"'
elif [[ -n "$PREBUILT_MODE" ]]; then
  echo 'node_preview_pg18_unknown_prebuilt_mode' >&2
  exit 2
fi

GO_CONTAINER_ID="$(docker create --name "$GO_CONTAINER" --label pandora.test=node-preview-pg18 --label "pandora.run=$RUN_ID" \
  --network "$NETWORK_ID" --cpus "$GO_CPUS" --memory "$GO_MEMORY" -v "$ROOT:/src:ro" -w /src \
  -e AEGIS_NODE_PREVIEW_PG18_FIXTURE=disposable-v1 \
  -e "AEGIS_NODE_PREVIEW_PG18_DSN=$APP_DSN" -e "AEGIS_NODE_PREVIEW_PG18_ADMIN_DSN=$ADMIN_DSN" \
  -e "AEGIS_NODE_PREVIEW_PG18_DATABASE=$DB_NAME" -e "AEGIS_NODE_PREVIEW_PG18_RUN_ID=$RUN_ID" \
  -e AEGIS_ANNOUNCEMENT_PG18_FIXTURE=disposable-v1 \
  -e "AEGIS_ANNOUNCEMENT_PG18_DSN=$APP_DSN" -e "AEGIS_ANNOUNCEMENT_PG18_ADMIN_DSN=$ADMIN_DSN" \
  -e "AEGIS_ANNOUNCEMENT_PG18_DATABASE=$DB_NAME" -e "AEGIS_ANNOUNCEMENT_PG18_RUN_ID=$RUN_ID" \
  -e AEGIS_SUPPORT_PG18_FIXTURE=disposable-v1 \
  -e "AEGIS_SUPPORT_PG18_DSN=$APP_DSN" -e "AEGIS_SUPPORT_PG18_ADMIN_DSN=$ADMIN_DSN" \
  -e "AEGIS_SUPPORT_PG18_DATABASE=$DB_NAME" -e "AEGIS_SUPPORT_PG18_RUN_ID=$RUN_ID" \
  "$GO_IMAGE_ID" sh -ec "$GO_COMMAND")"
set +e
GO_OUTPUT="$(docker start -a "$GO_CONTAINER_ID" 2>&1)"
GO_RC=$?
set -e
printf '%s\n' "$GO_OUTPUT"
[[ "$GO_RC" -eq 0 ]]
if [[ "$PREBUILT_MODE" == "announcement" ]]; then
  grep -Fq 'announcement_pg18_business=ok role=aegis_app rls=on schema=40' <<<"$GO_OUTPUT"
  echo 'announcement_pg18_prebuilt=ok cpu=0.25 memory=384m'
elif [[ "$PREBUILT_MODE" == "support" ]]; then
  grep -Fq 'support_pg18_business=ok role=aegis_app rls=on schema=40' <<<"$GO_OUTPUT"
  echo 'support_pg18_prebuilt=ok cpu=0.25 memory=384m'
else
  grep -Fq 'node_preview_pg18_business=ok role=aegis_app rls=on schema=40' <<<"$GO_OUTPUT"
  grep -Fq 'announcement_pg18_business=ok role=aegis_app rls=on schema=40' <<<"$GO_OUTPUT"
fi
BUSINESS_OK=1

printf 'node_preview_pg18_images=%s,%s\n' "$PG_IMAGE_ID" "$GO_IMAGE_ID"
printf 'node_preview_pg18_real_migrations=%d last=%s\n' "$migration_count" "$last_migration"
