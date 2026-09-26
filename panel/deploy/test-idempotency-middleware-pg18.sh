#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
SUFFIX="$RUN_ID"
PG_CONTAINER="pandora-idempotency-pg18-${SUFFIX}"
GO_CONTAINER="pandora-idempotency-go-${SUFFIX}"
NETWORK="pandora-idempotency-net-${SUFFIX}"
PG_DATA_VOLUME="pandora-idempotency-pgdata-${SUFFIX}"
MOD_VOLUME="pandora-idempotency-mod-${SUFFIX}"
DB_NAME="aegis_idempotency_test"
POSTGRES_PASSWORD="idempotency-postgres-test-only"
APP_PASSWORD="idempotency_app_test_password_2026_only"
DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable"
FAILPOINT="${PANDORA_IDEMPOTENCY_FAILPOINT:-}"
VOLUMES_BEFORE=0
ANONYMOUS_VOLUMES_BEFORE=""
CLEANUP_DONE=0
CLEANUP_STATUS=1
CLEANUP_RUNS=0
OWNED_MOUNT_SOURCES=()

cleanup() {
  if [[ "$CLEANUP_DONE" -eq 1 ]]; then
    return "$CLEANUP_STATUS"
  fi
  CLEANUP_DONE=1
  CLEANUP_RUNS=$((CLEANUP_RUNS + 1))
  set +e

  docker rm -fv "$GO_CONTAINER" >/dev/null 2>&1
  docker rm -fv "$PG_CONTAINER" >/dev/null 2>&1
  docker network rm "$NETWORK" >/dev/null 2>&1
  docker volume rm "$PG_DATA_VOLUME" >/dev/null 2>&1
  docker volume rm "$MOD_VOLUME" >/dev/null 2>&1

  local query_failed=0
  local container_names volume_names network_names
  local pg_remaining go_remaining containers_remaining
  local pg_data_remaining mod_remaining owned_volumes_remaining network_remaining
  local volumes_after volumes_delta
  local mount_sources_remaining=0
  local anonymous_after anonymous_new anonymous_new_count
  local source

  if ! docker info >/dev/null 2>&1; then
    query_failed=1
  fi
  if ! container_names="$(docker ps -a --format '{{.Names}}' 2>/dev/null)"; then
    query_failed=1
    container_names=""
  fi
  if ! volume_names="$(docker volume ls --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
    volume_names=""
  fi
  if ! network_names="$(docker network ls --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
    network_names=""
  fi

  pg_remaining="$(awk -v target="$PG_CONTAINER" '$0==target{n++} END{print n+0}' <<<"$container_names")"
  go_remaining="$(awk -v target="$GO_CONTAINER" '$0==target{n++} END{print n+0}' <<<"$container_names")"
  containers_remaining=$((pg_remaining + go_remaining))
  owned_volumes_remaining="$(awk -v target="$MOD_VOLUME" '$0==target{n++} END{print n+0}' <<<"$volume_names")"
  pg_data_remaining="$(awk -v target="$PG_DATA_VOLUME" '$0==target{n++} END{print n+0}' <<<"$volume_names")"
  mod_remaining="$owned_volumes_remaining"
  owned_volumes_remaining=$((pg_data_remaining + mod_remaining))
  network_remaining="$(awk -v target="$NETWORK" '$0==target{n++} END{print n+0}' <<<"$network_names")"
  volumes_after="$(awk 'NF{n++} END{print n+0}' <<<"$volume_names")"
  volumes_delta=$((volumes_after - VOLUMES_BEFORE))

  echo "mount_sources_post_cleanup_begin"
  for source in "${OWNED_MOUNT_SOURCES[@]}"; do
    if [[ -e "$source" ]]; then
      mount_sources_remaining=$((mount_sources_remaining + 1))
      echo "mount_source=${source} exists=1"
    else
      echo "mount_source=${source} exists=0"
    fi
  done
  echo "mount_sources_post_cleanup_end"

  anonymous_after="$(awk 'length($0)==64 && $0~/^[0-9a-f]+$/' <<<"$volume_names")"
  echo "anonymous_volumes_after_begin"
  printf '%s\n' "$anonymous_after" | awk 'NF'
  echo "anonymous_volumes_after_end"
  if [[ "$query_failed" -eq 0 ]]; then
    anonymous_new="$(comm -13 \
      <(printf '%s\n' "$ANONYMOUS_VOLUMES_BEFORE" | awk 'NF' | sort) \
      <(printf '%s\n' "$anonymous_after" | awk 'NF' | sort))"
    anonymous_new_count="$(awk 'NF{n++} END{print n+0}' <<<"$anonymous_new")"
  else
    anonymous_new_count=-1
  fi

  echo "containers_remaining=${containers_remaining}"
  echo "owned_volumes_remaining=${owned_volumes_remaining}"
  echo "network_remaining=${network_remaining}"
  echo "mount_sources_recorded=${#OWNED_MOUNT_SOURCES[@]}"
  echo "mount_sources_remaining=${mount_sources_remaining}"
  echo "anonymous_volumes_new_global=${anonymous_new_count}"
  echo "volumes_delta=${volumes_delta}"
  echo "cleanup_query_failed=${query_failed}"
  echo "cleanup_invocations=${CLEANUP_RUNS}"
  if [[ "$query_failed" -eq 0 && "$containers_remaining" -eq 0 &&
        "$owned_volumes_remaining" -eq 0 &&
        "$network_remaining" -eq 0 &&
        "$mount_sources_remaining" -eq 0 ]]; then
    CLEANUP_STATUS=0
    echo "cleanup=ok"
  else
    CLEANUP_STATUS=1
    echo "cleanup=failed"
  fi
  return "$CLEANUP_STATUS"
}

on_error() {
  local code=$?
  local command_kind="other"
  case "$BASH_COMMAND" in
    docker*) command_kind="docker" ;;
    awk*) command_kind="awk" ;;
    psql_admin*) command_kind="psql_admin" ;;
    up_sql*) command_kind="up_sql" ;;
    exit*) command_kind="exit" ;;
  esac
  echo "first_error_line=${BASH_LINENO[0]} exit=${code} command_kind=${command_kind}" >&2
  trap - ERR
  return "$code"
}

on_exit() {
  local original_status=$?
  local cleanup_status final_status
  trap - EXIT ERR INT TERM
  set +e
  cleanup
  cleanup_status=$?
  final_status=$original_status
  if [[ "$original_status" -eq 0 && "$cleanup_status" -ne 0 ]]; then
    final_status=1
  fi
  exit "$final_status"
}

on_signal() {
  local signal_status="$1"
  trap - EXIT ERR INT TERM
  set +e
  cleanup
  exit "$signal_status"
}

trap on_error ERR
trap on_exit EXIT
trap 'on_signal 130' INT
trap 'on_signal 143' TERM

command -v docker >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: docker is required" >&2
  exit 1
}
command -v awk >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: awk is required" >&2
  exit 1
}
command -v sha256sum >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: sha256sum is required" >&2
  exit 1
}
command -v comm >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: comm is required" >&2
  exit 1
}
command -v sort >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: sort is required" >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "test-idempotency-middleware-pg18: docker daemon query failed" >&2
  exit 1
}
VOLUMES_BEFORE="$(docker volume ls -q | wc -l | tr -d ' ')"
ANONYMOUS_VOLUMES_BEFORE="$(docker volume ls --format '{{.Name}}' |
  awk 'length($0)==64 && $0~/^[0-9a-f]+$/')"
echo "anonymous_volumes_before_begin"
printf '%s\n' "$ANONYMOUS_VOLUMES_BEFORE" | awk 'NF'
echo "anonymous_volumes_before_end"

up_sql() {
  awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1; next} up' "$1"
}

psql_admin() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" "$@"
}

assert_container_mount_contract() {
  local role="$1"
  local container="$2"
  local mounts labels image_digest
  local type name source destination read_write
  local total=0
  local volume_count=0
  local bind_count=0
  local contract_failed=0

  mounts="$(docker inspect -f \
    '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' \
    "$container")"
  labels="$(docker inspect -f \
    '{{index .Config.Labels "pandora.test"}}|{{index .Config.Labels "pandora.run"}}' \
    "$container")"
  image_digest="$(docker inspect -f '{{.Image}}' "$container")"

  echo "actual_mounts_begin role=${role}"
  while IFS='|' read -r type name source destination read_write; do
    [[ -n "$type" ]] || continue
    total=$((total + 1))
    echo "mount role=${role} type=${type} name=${name} source=${source} destination=${destination} rw=${read_write}"
    if [[ "$type" == "volume" ]]; then
      volume_count=$((volume_count + 1))
      OWNED_MOUNT_SOURCES+=("$source")
    elif [[ "$type" == "bind" ]]; then
      bind_count=$((bind_count + 1))
    fi

    case "$role" in
      pg)
        if [[ "$type" != "volume" || "$name" != "$PG_DATA_VOLUME" ||
              "$destination" != "/var/lib/postgresql" || "$read_write" != "true" ]]; then
          contract_failed=1
        fi
        ;;
      go)
        if [[ "$type" == "volume" ]]; then
          if [[ "$name" != "$MOD_VOLUME" || "$destination" != "/go/pkg/mod" ||
                "$read_write" != "true" ]]; then
            contract_failed=1
          fi
        elif [[ "$type" == "bind" ]]; then
          if [[ "$source" != "$ROOT" || "$destination" != "/src" ||
                "$read_write" != "false" ]]; then
            contract_failed=1
          fi
        else
          contract_failed=1
        fi
        ;;
      *)
        contract_failed=1
        ;;
    esac
  done <<<"$mounts"
  echo "actual_mounts_end role=${role}"

  if [[ "$labels" != "idempotency-pg18|${RUN_ID}" ]]; then
    contract_failed=1
  fi
  case "$role" in
    pg)
      if [[ "$total" -ne 1 || "$volume_count" -ne 1 || "$bind_count" -ne 0 ]]; then
        contract_failed=1
      fi
      ;;
    go)
      if [[ "$total" -ne 2 || "$volume_count" -ne 1 || "$bind_count" -ne 1 ]]; then
        contract_failed=1
      fi
      ;;
  esac
  if [[ "$contract_failed" -ne 0 ]]; then
    echo "test-idempotency-middleware-pg18: ${role} mount contract failed" >&2
    return 1
  fi
  echo "marker=idempotency_pg18_${role}_mount_contract_ok image_digest=${image_digest}"
}

docker network create \
  --label "pandora.test=idempotency-pg18" \
  --label "pandora.run=${RUN_ID}" \
  "$NETWORK" >/dev/null
docker volume create \
  --label "pandora.test=idempotency-pg18" \
  --label "pandora.run=${RUN_ID}" \
  "$PG_DATA_VOLUME" >/dev/null
docker volume create \
  --label "pandora.test=idempotency-pg18" \
  --label "pandora.run=${RUN_ID}" \
  "$MOD_VOLUME" >/dev/null

network_labels="$(docker network inspect -f \
  '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$NETWORK")"
pg_volume_labels="$(docker volume inspect -f \
  '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$PG_DATA_VOLUME")"
mod_volume_labels="$(docker volume inspect -f \
  '{{index .Labels "pandora.test"}}|{{index .Labels "pandora.run"}}' "$MOD_VOLUME")"
if [[ "$network_labels" != "idempotency-pg18|${RUN_ID}" ||
      "$pg_volume_labels" != "idempotency-pg18|${RUN_ID}" ||
      "$mod_volume_labels" != "idempotency-pg18|${RUN_ID}" ]]; then
  echo "test-idempotency-middleware-pg18: resource labels are not traceable" >&2
  exit 1
fi
echo "marker=idempotency_pg18_resource_labels_ok run_id=${RUN_ID}"

docker run -d --name "$PG_CONTAINER" \
  --label "pandora.test=idempotency-pg18" \
  --label "pandora.run=${RUN_ID}" \
  --network "$NETWORK" --network-alias pg18 \
  -v "$PG_DATA_VOLUME:/var/lib/postgresql" \
  -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
  -e POSTGRES_DB="$DB_NAME" \
  "$POSTGRES_IMAGE" >/dev/null

pg_declared_volume="$(docker image inspect -f \
  '{{if index .Config.Volumes "/var/lib/postgresql"}}yes{{else}}no{{end}}' \
  "$POSTGRES_IMAGE")"
if [[ "$pg_declared_volume" != "yes" ]]; then
  echo "test-idempotency-middleware-pg18: PG image does not declare expected parent data path" >&2
  exit 1
fi
assert_container_mount_contract pg "$PG_CONTAINER"
echo "marker=idempotency_pg18_image_declared_path_ok path=/var/lib/postgresql"

docker create --name "$GO_CONTAINER" \
  --label "pandora.test=idempotency-pg18" \
  --label "pandora.run=${RUN_ID}" \
  --network "$NETWORK" \
  -v "$ROOT:/src:ro" \
  -v "$MOD_VOLUME:/go/pkg/mod" \
  -w /src \
  -e "AEGIS_IDEMPOTENCY_PG18_DSN=$DSN" \
  "$GO_IMAGE" sh -ec '
    go version
    go test -v -count=1 -timeout=45s -run "^TestIdempotencyMiddlewarePG18$" ./internal/middleware
  ' >/dev/null
assert_container_mount_contract go "$GO_CONTAINER"

case "$FAILPOINT" in
  "") ;;
  after_resources)
    echo "marker=idempotency_pg18_failpoint_after_resources"
    exit 97
    ;;
  wait_after_resources)
    echo "marker=idempotency_pg18_failpoint_wait_ready"
    while :; do
      sleep 1
    done
    ;;
  *)
    echo "test-idempotency-middleware-pg18: unknown failpoint" >&2
    exit 2
    ;;
esac

for _ in $(seq 1 90); do
  if docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "$PG_CONTAINER" pg_isready -U postgres -d "$DB_NAME" >/dev/null

port_bindings="$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER")"
if [[ "$port_bindings" != "{}" && "$port_bindings" != "null" ]]; then
  echo "postgres unexpectedly exposes host ports: $port_bindings" >&2
  exit 1
fi
echo "marker=idempotency_pg18_no_host_port_ok"
echo "postgres_version=$(docker exec "$PG_CONTAINER" postgres --version)"

migration_count=0
for migration in "$ROOT"/migrations/000{01..36}_*.sql; do
  if [[ ! -f "$migration" ]]; then
    echo "missing migration in 1..36 sequence: $migration" >&2
    exit 1
  fi
  up_sql "$migration" | psql_admin >/dev/null
  migration_count=$((migration_count + 1))
done
if [[ "$migration_count" -ne 36 ]]; then
  echo "expected 36 migrations, applied $migration_count" >&2
  exit 1
fi
echo "marker=idempotency_pg18_real_migrations_1_36_ok"

docker exec -i \
  -e PGPASSWORD="$POSTGRES_PASSWORD" \
  -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
  "$PG_CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null
echo "marker=idempotency_pg18_configure_app_role_ok"

# Legacy fixtures must be created with the real schema-36 guard enabled. They
# model rows written by the pre-schema-37 runtime; schema37 must only classify
# and protect them, never manufacture them after its actor-only guard exists.
psql_admin >/dev/null <<'SQL'
DO $guard_catalog$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_trigger
     WHERE tgrelid='public.idempotency_keys'::regclass
       AND tgname='zz_idempotency_evidence_guard'
       AND NOT tgisinternal AND tgenabled='O'
  ) THEN
    RAISE EXCEPTION 'schema36 idempotency evidence guard is not enabled';
  END IF;
END
$guard_catalog$;

INSERT INTO tenants (id,slug,display_name,default_currency)
VALUES
  ('91000000-0000-7000-8000-000000000001','idem-pg18-a','Idempotency PG18 A','USD'),
  ('92000000-0000-7000-8000-000000000001','idem-pg18-b','Idempotency PG18 B','USD');

INSERT INTO users (id,tenant_id,email,display_name,status)
VALUES
  ('91000000-0000-7000-8000-000000000011','91000000-0000-7000-8000-000000000001',
   'idem-a1@example.test','Idem A1','active'),
  ('91000000-0000-7000-8000-000000000012','91000000-0000-7000-8000-000000000001',
   'idem-a2@example.test','Idem A2','active'),
  ('92000000-0000-7000-8000-000000000011','92000000-0000-7000-8000-000000000001',
   'idem-b1@example.test','Idem B1','active');

DO $guard_negative$
BEGIN
  BEGIN
    INSERT INTO idempotency_keys
      (tenant_id,scope,idempotency_key,request_hash,locked_until,status,
       response_code,completed_at,resource_type,resource_id)
    VALUES
      ('91000000-0000-7000-8000-000000000001','pg18_guard_negative',
       'guard-negative-key',decode(repeat('00',32),'hex'),now()+interval '5 minutes',
       'succeeded',200,now(),'order','91000000-0000-7000-8000-000000000099');
    RAISE EXCEPTION 'schema36 idempotency evidence guard allowed an illegal insert';
  EXCEPTION WHEN check_violation THEN
    IF position('idempotency claims must start unbound and in_flight' in SQLERRM)=0 THEN
      RAISE;
    END IF;
  END;
END
$guard_negative$;

-- The four raw rows cover recent/ancient and in-flight/terminal legacy owners.
-- They are inserted normally; the evidence guard is never disabled or bypassed.
INSERT INTO idempotency_keys
  (tenant_id,scope,idempotency_key,request_hash,locked_until,created_at,expires_at)
VALUES
  ('91000000-0000-7000-8000-000000000001','pg18_legacy_probe',
   'legacy-probe-ancient-inflight',decode(repeat('00',32),'hex'),now()-interval '400 days',
   now()-interval '400 days',now()-interval '370 days'),
  ('91000000-0000-7000-8000-000000000001','pg18_legacy_probe',
   'legacy-probe-recent-inflight',decode(repeat('01',32),'hex'),now()+interval '5 minutes',
   now(),now()+interval '30 days'),
  ('91000000-0000-7000-8000-000000000001','pg18_legacy_probe',
   'legacy-probe-ancient-terminal',decode(repeat('02',32),'hex'),now()-interval '400 days',
   now()-interval '400 days',now()-interval '370 days'),
  ('91000000-0000-7000-8000-000000000001','pg18_legacy_probe',
   'legacy-probe-recent-terminal',decode(repeat('03',32),'hex'),now()+interval '5 minutes',
   now(),now()+interval '30 days');

UPDATE idempotency_keys
   SET status='succeeded',response_code=200,response_body='{"legacy":true}'::jsonb,
       completed_at=CASE
         WHEN idempotency_key='legacy-probe-ancient-terminal'
           THEN now()-interval '390 days'
         ELSE now()
       END,
       locked_until=NULL
 WHERE tenant_id='91000000-0000-7000-8000-000000000001'
   AND scope='pg18_legacy_probe'
   AND idempotency_key IN
       ('legacy-probe-ancient-terminal','legacy-probe-recent-terminal');

-- A genuine schema-36 actor-bound terminal row must remain replayable after
-- schema37 converts response_body to response_format=legacy_json.
INSERT INTO idempotency_keys
  (tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
SELECT
  '91000000-0000-7000-8000-000000000001',
  'pg18_legacy_actor_replay:actor:' ||
    substr(encode(public.digest(
      '91000000-0000-7000-8000-000000000011','sha256'
    ),'hex'),1,24),
  'legacy-actor-replay-key',
  public.digest(convert_to(
    'POST' || chr(10) || '/pg18/legacy-actor?variant=1' || chr(10) ||
    '{"legacy":"request"}', 'UTF8'
  ),'sha256'),
  '91000000-0000-7000-8000-000000000011',now()+interval '5 minutes';

UPDATE idempotency_keys
   SET status='succeeded',response_code=201,
       response_body='{"legacy":true,"source":"schema36"}'::jsonb,
       completed_at=now(),locked_until=NULL
 WHERE tenant_id='91000000-0000-7000-8000-000000000001'
   AND scope LIKE 'pg18_legacy_actor_replay:actor:%'
   AND idempotency_key='legacy-actor-replay-key';
SQL
echo "marker=idempotency_pg18_schema36_guarded_legacy_fixtures_ok"

schema37_migration="$ROOT/migrations/00037_idempotency_runtime_hardening.sql"
if [[ ! -f "$schema37_migration" ]]; then
  echo "missing migration 00037: $schema37_migration" >&2
  exit 1
fi
{
  echo "BEGIN;"
  echo "SELECT set_config('app.idempotency_writers_stopped', 'yes', true);"
  echo "SELECT set_config('app.allow_idempotency_schema37_up', 'yes', true);"
  up_sql "$schema37_migration"
  echo "COMMIT;"
} | psql_admin >/dev/null
echo "marker=idempotency_pg18_real_migration_37_approved_ok"

# Re-assert the runtime role after schema37 creates/replaces objects and ACLs.
docker exec -i \
  -e PGPASSWORD="$POSTGRES_PASSWORD" \
  -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
  "$PG_CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
  < "$ROOT/deploy/configure-app-role.sql" >/dev/null
echo "marker=idempotency_pg18_configure_app_role_schema37_ok"
echo "marker=idempotency_pg18_schema37_fixture_conversion_ok"

psql_admin >/dev/null <<'SQL'
CREATE OR REPLACE FUNCTION public.aegis_test_failed_row_lock_gate()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog
AS $function$
BEGIN
  IF OLD.tenant_id = '91000000-0000-7000-8000-000000000001'
     AND OLD.actor_id = '91000000-0000-7000-8000-000000000011'
     AND OLD.scope LIKE 'pg18_failed_lock_race:actor:%'
     AND OLD.idempotency_key = 'failed-lock-race-key'
     AND OLD.status = 'failed'
     AND NEW.status = 'in_flight' THEN
    PERFORM pg_catalog.pg_advisory_xact_lock(910036, 2201);
  END IF;
  RETURN NEW;
END
$function$;

REVOKE ALL ON FUNCTION public.aegis_test_failed_row_lock_gate() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.aegis_test_failed_row_lock_gate() TO aegis_app;

CREATE TRIGGER aegis_test_failed_row_lock_gate
BEFORE UPDATE ON public.idempotency_keys
FOR EACH ROW
EXECUTE FUNCTION public.aegis_test_failed_row_lock_gate();
SQL
echo "marker=idempotency_pg18_failed_row_lock_fixture_ok"

echo "source_hashes_begin"
sha256sum \
  "$ROOT/internal/middleware/idempotency.go" \
  "$ROOT/internal/middleware/idempotency_replay.go" \
  "$ROOT/internal/middleware/idempotency_recorder.go" \
  "$ROOT/internal/middleware/idempotency_test.go" \
  "$ROOT/internal/middleware/idempotency_recorder_test.go" \
  "$ROOT/internal/middleware/idempotency_capture_test.go" \
  "$ROOT/internal/middleware/idempotency_replay_test.go" \
  "$ROOT/internal/middleware/idempotency_pg18_test.go" \
  "$ROOT/internal/middleware/idempotency_pg18_fixture_test.go" \
  "$ROOT/migrations/00037_idempotency_runtime_hardening.sql" \
  "$ROOT/deploy/configure-app-role.sql" \
  "$ROOT/deploy/test-idempotency-middleware-pg18.sh"
echo "source_hashes_end"

set +e
docker start -a "$GO_CONTAINER"
go_test_exit=$?
set -e
echo "go_test_exit=${go_test_exit}"
if [[ "$go_test_exit" -ne 0 ]]; then
  exit "$go_test_exit"
fi
echo "marker=idempotency_middleware_pg18_suite_ok"
