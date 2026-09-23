#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
CONTAINER="pandora-idem38-${RUN_ID}"
NETWORK="pandora-idem38-net-${RUN_ID}"
PG_VOLUME="pandora-idem38-pg-${RUN_ID}"
LABEL_KEY="pandora.idempotency-00038.run"
PASSWORD="idempotency-38-test-only"
LOG="$(mktemp)"
AUDIT_LOG="${PANDORA_TEST_AUDIT_LOG:-}"
VOLUMES_BEFORE="$(mktemp)"
DANGLING_BEFORE="$(mktemp)"
FIRST_ERROR_LINE=""
BASELINE_QUERY_FAILED=0
OWNED_MOUNT_SOURCES=()
chmod 0600 "$LOG" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"

command -v docker >/dev/null 2>&1 || { echo 'docker is required' >&2; exit 1; }
command -v awk >/dev/null 2>&1 || { echo 'awk is required' >&2; exit 1; }

record_error() {
  local rc=$?
  if [[ -z "$FIRST_ERROR_LINE" ]]; then
    FIRST_ERROR_LINE="line=$1 rc=$rc command=$2"
    printf '%s\n' "$FIRST_ERROR_LINE" >>"$LOG"
  fi
  return "$rc"
}
trap 'record_error "$LINENO" "$BASH_COMMAND"' ERR

cleanup() {
  local main_rc="$1" cleanup_rc=0 query_failed="$BASELINE_QUERY_FAILED"
  local container_names="" network_names="" volume_names=""
  local label_containers="" label_networks="" label_volumes=""
  local container_remaining=0 network_remaining=0 volume_remaining=0
  local label_remaining=0 mount_sources_remaining=0
  local global_delta_all=-1 global_delta_dangling=-1 anonymous_new=-1
  local new_all="" new_dangling="" source
  trap - EXIT ERR INT TERM
  set +e
  docker rm -fv "$CONTAINER" >/dev/null 2>&1
  docker network rm "$NETWORK" >/dev/null 2>&1
  docker volume rm "$PG_VOLUME" >/dev/null 2>&1
  container_names="$(docker ps -a --format '{{.Names}}' 2>/dev/null)" || query_failed=1
  network_names="$(docker network ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  volume_names="$(docker volume ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  label_containers="$(docker ps -a --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Names}}' 2>/dev/null)" || query_failed=1
  label_networks="$(docker network ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  label_volumes="$(docker volume ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  container_remaining="$(awk -v target="$CONTAINER" '$0==target{n++} END{print n+0}' <<<"$container_names")"
  network_remaining="$(awk -v target="$NETWORK" '$0==target{n++} END{print n+0}' <<<"$network_names")"
  volume_remaining="$(awk -v target="$PG_VOLUME" '$0==target{n++} END{print n+0}' <<<"$volume_names")"
  label_remaining=$((
    $(awk 'NF{n++} END{print n+0}' <<<"$label_containers") +
    $(awk 'NF{n++} END{print n+0}' <<<"$label_networks") +
    $(awk 'NF{n++} END{print n+0}' <<<"$label_volumes")
  ))
  for source in "${OWNED_MOUNT_SOURCES[@]}"; do
    [[ ! -e "$source" ]] || mount_sources_remaining=$((mount_sources_remaining + 1))
  done
  printf '%s\n' "$volume_names" | awk 'NF' | sort >"$LOG.all.after" || query_failed=1
  docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$LOG.dangling.after" || query_failed=1
  if [[ "$query_failed" -eq 0 ]]; then
    new_all="$(comm -13 "$VOLUMES_BEFORE" "$LOG.all.after")"
    new_dangling="$(comm -13 "$DANGLING_BEFORE" "$LOG.dangling.after")"
    global_delta_all="$(awk 'NF{n++} END{print n+0}' <<<"$new_all")"
    global_delta_dangling="$(awk 'NF{n++} END{print n+0}' <<<"$new_dangling")"
    anonymous_new="$(awk 'length($0)==64 && $0~/^[0-9a-f]+$/{n++} END{print n+0}' <<<"$new_all")"
  fi
  if [[ "$query_failed" -ne 0 || "$container_remaining" -ne 0 ||
        "$network_remaining" -ne 0 || "$volume_remaining" -ne 0 ||
        "$label_remaining" -ne 0 || "$mount_sources_remaining" -ne 0 ||
        "$anonymous_new" -ne 0 ]]; then cleanup_rc=1; fi
  printf 'cleanup_query_failed=%s exact_name_container=%s exact_name_network=%s exact_name_volume=%s exact_label_residual=%s mount_source_residual=%s\n' \
    "$query_failed" "$container_remaining" "$network_remaining" "$volume_remaining" \
    "$label_remaining" "$mount_sources_remaining" >&3
  printf 'global_volume_delta_all=%s global_volume_delta_dangling=%s anonymous_volume_new=%s\n' \
    "$global_delta_all" "$global_delta_dangling" "$anonymous_new" >&3
  if grep -Fq "$PASSWORD" "$LOG"; then echo 'secret scan failed' >&4; cleanup_rc=1; fi
  grep -E '^[a-z0-9_]+=ok$' "$LOG" | sort -u >&3 || true
  printf 'audit_log_sha256=%s\n' "$(sha256sum "$LOG" | awk '{print $1}')" >&3
  [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error_line=%s\n' "$FIRST_ERROR_LINE" >&4
  if [[ -n "$AUDIT_LOG" ]]; then
    install -m 0600 "$LOG" "$AUDIT_LOG" || cleanup_rc=1
    printf 'audit_log_path=%s\n' "$AUDIT_LOG" >&4
  fi
  rm -f "$LOG" "$LOG.all.after" "$LOG.dangling.after" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"
  if [[ "$main_rc" -eq 0 && "$cleanup_rc" -ne 0 ]]; then main_rc="$cleanup_rc"; fi
  exit "$main_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

exec 3>&1 4>&2
exec >>"$LOG" 2>&1
docker volume ls --format '{{.Name}}' 2>/dev/null | sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$DANGLING_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]] || exit 1

IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$IMAGE")"
[[ "$(docker image inspect -f '{{json .Config.Volumes}}' "$IMAGE_ID")" == '{"/var/lib/postgresql":{}}' ]] || {
  echo 'unexpected postgres image volume contract' >&2; exit 1;
}
docker network create --label "$LABEL_KEY=$RUN_ID" "$NETWORK" >/dev/null
docker volume create --label "$LABEL_KEY=$RUN_ID" "$PG_VOLUME" >/dev/null
docker create --name "$CONTAINER" --network "$NETWORK" --label "$LABEL_KEY=$RUN_ID" \
  -e POSTGRES_PASSWORD="$PASSWORD" -e POSTGRES_DB=idem38 \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" "$IMAGE_ID" >/dev/null

mount_rows="$(docker inspect -f '{{range .Mounts}}{{println .Type "|" .Name "|" .Source "|" .Destination "|" .RW}}{{end}}' "$CONTAINER")"
mapfile -t mounts < <(printf '%s\n' "$mount_rows" | sed '/^[[:space:]]*$/d')
[[ "${#mounts[@]}" -eq 2 ]]
printf '%s\n' "${mounts[@]}" | grep -Fq "volume | $PG_VOLUME | "
printf '%s\n' "${mounts[@]}" | grep -Fq '| /var/lib/postgresql | true'
printf '%s\n' "${mounts[@]}" | grep -Fq "bind |  | $ROOT | /src | false"
volume_mount_source="$(printf '%s\n' "${mounts[@]}" | awk -F'|' -v target="$PG_VOLUME" '
  {for(i=1;i<=NF;i++){gsub(/^[[:space:]]+|[[:space:]]+$/,"",$i)}}
  $1=="volume" && $2==target {print $3}')"
[[ -n "$volume_mount_source" ]]
OWNED_MOUNT_SOURCES+=("$volume_mount_source")

docker start "$CONTAINER" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$CONTAINER" pg_isready -U postgres -d idem38 >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -d idem38 >/dev/null

psql_db() {
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d idem38 "$@"
}
psql_app() {
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -U aegis_app -d idem38 "$@"
}
up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
down_sql() { awk '/^-- \+goose Down/{down=1;next} down' "$1"; }
apply37() {
  { echo 'BEGIN;'; echo "SET LOCAL app.idempotency_writers_stopped='yes';";
    echo "SET LOCAL app.allow_idempotency_schema37_up='yes';";
    up_sql "$ROOT/migrations/00037_idempotency_runtime_hardening.sql"; echo 'COMMIT;'; } | psql_db
}
apply38() {
  { echo 'BEGIN;'; echo "SET LOCAL app.idempotency_writers_stopped='yes';";
    echo "SET LOCAL app.allow_idempotency_schema38_up='yes';";
    up_sql "$ROOT/migrations/00038_idempotency_resource_binding.sql"; echo 'COMMIT;'; } | psql_db
}
down38() {
  { echo 'BEGIN;'; echo "SET LOCAL app.idempotency_writers_stopped='yes';";
    echo "SET LOCAL app.idempotency_binder_callers_stopped='yes';";
    echo "SET LOCAL app.allow_idempotency_schema38_down='yes';";
    down_sql "$ROOT/migrations/00038_idempotency_resource_binding.sql"; echo 'COMMIT;'; } | psql_db
}
configure_role() {
  docker exec -i -e PGPASSWORD="$PASSWORD" -e AEGIS_DB_APP_PASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d idem38 -f /src/deploy/configure-app-role.sql >/dev/null
}

for migration in "$ROOT"/migrations/000{01..36}_*.sql; do up_sql "$migration" | psql_db >/dev/null; done
apply37 >/dev/null
configure_role
apply38 >/dev/null
configure_role

psql_db <<'SQL'
DO $$
DECLARE v_owner oid; v_proc oid;
BEGIN
  SELECT oid INTO v_owner FROM pg_authid WHERE rolname='aegis_idempotency_owner'
    AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
    AND NOT rolinherit AND NOT rolreplication AND NOT rolbypassrls
    AND rolpassword IS NULL;
  SELECT oid INTO v_proc FROM pg_proc WHERE oid=
    'app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)'::regprocedure
    AND proowner=v_owner AND prosecdef AND provolatile='v'
    AND proconfig IS NOT DISTINCT FROM ARRAY['search_path=pg_catalog'];
  IF v_owner IS NULL OR v_proc IS NULL
     OR EXISTS(SELECT 1 FROM pg_auth_members WHERE roleid=v_owner OR member=v_owner)
     OR EXISTS(SELECT 1 FROM pg_db_role_setting WHERE setrole=v_owner)
     OR has_function_privilege('public',v_proc,'EXECUTE')
     OR NOT has_function_privilege('aegis_app',v_proc,'EXECUTE')
     OR NOT has_function_privilege('aegis_idempotency_owner',
          'app.idempotency_scope_matches_actor(text,uuid)','EXECUTE')
     OR has_table_privilege('aegis_idempotency_owner','idempotency_keys','SELECT')
     OR has_table_privilege('aegis_idempotency_owner','idempotency_keys','UPDATE')
     OR has_column_privilege('aegis_app','idempotency_keys','resource_type','UPDATE') THEN
    RAISE EXCEPTION 'schema38 catalog or ACL gate failed';
  END IF;
END $$;
SELECT 'idempotency_00038_catalog_acl=ok';
SQL

# A never-used binder must be exactly reversible, and re-applicable.
down38 >/dev/null
psql_db -Atc "SELECT to_regprocedure('app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)') IS NULL" | grep -qx t
echo 'idempotency_00038_clean_down=ok'
apply38 >/dev/null
configure_role

psql_db >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name,default_currency) VALUES
 ('91000000-0000-7000-8000-000000000001','idem38-a','Idem 38 A','CNY'),
 ('92000000-0000-7000-8000-000000000001','idem38-b','Idem 38 B','CNY');
INSERT INTO users(id,tenant_id,email,display_name) VALUES
 ('91000000-0000-7000-8000-000000000011','91000000-0000-7000-8000-000000000001','a@idem38.test','A'),
 ('91000000-0000-7000-8000-000000000012','91000000-0000-7000-8000-000000000001','a2@idem38.test','A2'),
 ('92000000-0000-7000-8000-000000000011','92000000-0000-7000-8000-000000000001','b@idem38.test','B');
INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
  app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011'),
  'rollback-topup',digest('rollback-topup','sha256'),'in_flight',
  '91000000-0000-7000-8000-000000000011','2026-07-30 20:00:00+00'),
 ('91000000-0000-7000-8000-000000000102','91000000-0000-7000-8000-000000000001',
  app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011'),
  'race-topup',digest('race-topup','sha256'),'in_flight',
  '91000000-0000-7000-8000-000000000011','2026-07-30 20:05:00+00');
SQL

psql_app <<'SQL'
SELECT set_config('app.tenant_id','91000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','91000000-0000-7000-8000-000000000011',false);
DO $$
DECLARE v_scope text:=app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011');
        v_other_scope text:=app.idempotency_actor_scope('subscription_renewal_create','91000000-0000-7000-8000-000000000011');
        v_rows int;
BEGIN
  BEGIN
    UPDATE idempotency_keys SET resource_type='order',resource_id='91000000-0000-7000-8000-000000000201'
     WHERE id='91000000-0000-7000-8000-000000000101';
    RAISE EXCEPTION 'direct resource UPDATE unexpectedly succeeded';
  EXCEPTION WHEN insufficient_privilege THEN NULL;
  END;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000999','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'claim id mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','92000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'tenant mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000012','balance_topup_create',
   app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000012'),
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'actor mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','subscription_renewal_create',v_other_scope,
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'base/storage scope mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'wrong-key',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'key mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'rollback-topup',digest('wrong-hash','sha256'),1,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'hash mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'rollback-topup',digest('rollback-topup','sha256'),2,'2026-07-30 20:00:00+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'generation mismatch bound'; END IF;
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',v_scope,
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:01+00','order','91000000-0000-7000-8000-000000000201');
  IF v_rows<>0 THEN RAISE EXCEPTION 'lease mismatch bound'; END IF;
END $$;
SELECT 'idempotency_00038_full_tuple_zero_rows=ok';

BEGIN;
SELECT set_config('app.tenant_id','91000000-0000-7000-8000-000000000001',true),
       set_config('app.actor_id','91000000-0000-7000-8000-000000000011',true);
DO $$
DECLARE v_order uuid; v_reservation uuid; v_rows int;
BEGIN
  INSERT INTO orders(tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
   discount_amount,tax_amount,balance_applied,total_amount,payable_amount,expires_at,idempotency_key_id)
  VALUES('91000000-0000-7000-8000-000000000001','IDEM38-ROLLBACK',
   '91000000-0000-7000-8000-000000000011','topup','draft','CNY',100,0,0,0,100,100,
   now()+interval '15 minutes','91000000-0000-7000-8000-000000000101')
  RETURNING id INTO v_order;
  INSERT INTO order_reservations(tenant_id,order_id,user_id,expires_at)
  VALUES('91000000-0000-7000-8000-000000000001',v_order,
    '91000000-0000-7000-8000-000000000011',now()+interval '15 minutes')
  RETURNING id INTO v_reservation;
  INSERT INTO order_reservation_events
    (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
     business_request_id,actor_kind,actor_id)
  VALUES('91000000-0000-7000-8000-000000000001',v_reservation,v_order,NULL,
    'held','reserve','91000000-0000-7000-8000-000000000101','user',
    '91000000-0000-7000-8000-000000000011');
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',
   app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011'),
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order',v_order);
  IF v_rows<>1 THEN RAISE EXCEPTION 'rollback bind lost'; END IF;
END $$;
SET CONSTRAINTS ALL IMMEDIATE;
ROLLBACK;
SQL

psql_db -At <<'SQL' | grep -qx 't|t'
SELECT resource_id IS NULL,(SELECT NOT used FROM app.idempotency_resource_binding_00038_usage)
 FROM idempotency_keys WHERE id='91000000-0000-7000-8000-000000000101';
SQL
echo 'idempotency_00038_watermark_rollback=ok'

psql_app >/dev/null <<'SQL'
BEGIN;
SELECT set_config('app.tenant_id','91000000-0000-7000-8000-000000000001',true),
       set_config('app.actor_id','91000000-0000-7000-8000-000000000011',true);
DO $$
DECLARE v_order uuid; v_reservation uuid; v_rows int;
BEGIN
  INSERT INTO orders(tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
   discount_amount,tax_amount,balance_applied,total_amount,payable_amount,expires_at,idempotency_key_id)
  VALUES('91000000-0000-7000-8000-000000000001','IDEM38-COMMIT',
   '91000000-0000-7000-8000-000000000011','topup','draft','CNY',100,0,0,0,100,100,
   now()+interval '15 minutes','91000000-0000-7000-8000-000000000101')
  RETURNING id INTO v_order;
  INSERT INTO order_reservations(tenant_id,order_id,user_id,expires_at)
  VALUES('91000000-0000-7000-8000-000000000001',v_order,
    '91000000-0000-7000-8000-000000000011',now()+interval '15 minutes')
  RETURNING id INTO v_reservation;
  INSERT INTO order_reservation_events
    (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
     business_request_id,actor_kind,actor_id)
  VALUES('91000000-0000-7000-8000-000000000001',v_reservation,v_order,NULL,
    'held','reserve','91000000-0000-7000-8000-000000000101','user',
    '91000000-0000-7000-8000-000000000011');
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000101','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',
   app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011'),
   'rollback-topup',digest('rollback-topup','sha256'),1,'2026-07-30 20:00:00+00','order',v_order);
  IF v_rows<>1 THEN RAISE EXCEPTION 'commit bind lost'; END IF;
END $$;
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
psql_db -Atc "SELECT used FROM app.idempotency_resource_binding_00038_usage" | grep -qx t
echo 'idempotency_00038_rls_topup_commit=ok'

# Re-running bootstrap must not restore direct resource UPDATE.
configure_role
psql_db -At <<'SQL' | grep -qx 'f|f|t'
SELECT has_column_privilege('aegis_app','idempotency_keys','resource_type','UPDATE'),
       has_column_privilege('aegis_app','idempotency_keys','resource_id','UPDATE'),
       has_function_privilege('aegis_idempotency_owner',
         'app.idempotency_scope_matches_actor(text,uuid)','EXECUTE');
SQL
echo 'idempotency_00038_configure_rerun_acl=ok'

race_bind() {
  local resource_id="$1" order_no="$2"
  psql_app >/dev/null <<SQL
BEGIN;
SELECT set_config('app.tenant_id','91000000-0000-7000-8000-000000000001',true),
       set_config('app.actor_id','91000000-0000-7000-8000-000000000011',true);
DO \$\$
DECLARE v_rows int; v_reservation uuid;
BEGIN
  SELECT count(*) INTO v_rows FROM app.bind_idempotency_resource(
   '91000000-0000-7000-8000-000000000102','91000000-0000-7000-8000-000000000001',
   '91000000-0000-7000-8000-000000000011','balance_topup_create',
   app.idempotency_actor_scope('balance_topup_create','91000000-0000-7000-8000-000000000011'),
   'race-topup',digest('race-topup','sha256'),1,'2026-07-30 20:05:00+00','order','$resource_id');
  IF v_rows=1 THEN
    PERFORM pg_sleep(1);
    INSERT INTO orders(id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
      discount_amount,tax_amount,balance_applied,total_amount,payable_amount,
      expires_at,idempotency_key_id)
    VALUES('$resource_id','91000000-0000-7000-8000-000000000001','$order_no',
      '91000000-0000-7000-8000-000000000011','topup','draft','CNY',50,0,0,0,50,50,
      now()+interval '15 minutes','91000000-0000-7000-8000-000000000102');
    INSERT INTO order_reservations(tenant_id,order_id,user_id,expires_at)
    VALUES('91000000-0000-7000-8000-000000000001','$resource_id',
      '91000000-0000-7000-8000-000000000011',now()+interval '15 minutes')
    RETURNING id INTO v_reservation;
    INSERT INTO order_reservation_events
      (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
       business_request_id,actor_kind,actor_id)
    VALUES('91000000-0000-7000-8000-000000000001',v_reservation,'$resource_id',NULL,
      'held','reserve','91000000-0000-7000-8000-000000000102','user',
      '91000000-0000-7000-8000-000000000011');
  ELSE
    RAISE EXCEPTION 'lost concurrent idempotency claim' USING ERRCODE='40001';
  END IF;
END \$\$;
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
}
psql_db -c 'GRANT INSERT(id) ON orders TO aegis_app' >/dev/null
race_bind '91000000-0000-7000-8000-000000000202' 'IDEM38-RACE-A' & race_a=$!
race_bind '91000000-0000-7000-8000-000000000203' 'IDEM38-RACE-B' & race_b=$!
if wait "$race_a"; then race_a_rc=0; else race_a_rc=$?; fi
if wait "$race_b"; then race_b_rc=0; else race_b_rc=$?; fi
psql_db -c 'REVOKE INSERT(id) ON orders FROM aegis_app' >/dev/null
[[ $(( (race_a_rc==0) + (race_b_rc==0) )) -eq 1 ]]
psql_db -At <<'SQL' | grep -qx '1|1|t'
SELECT count(*) FILTER (WHERE o.order_no IN ('IDEM38-RACE-A','IDEM38-RACE-B')),
       count(DISTINCT resource_id),
       bool_and(resource_id=o.id)
  FROM idempotency_keys k
  LEFT JOIN orders o ON o.tenant_id=k.tenant_id AND o.id=k.resource_id
 WHERE k.id='91000000-0000-7000-8000-000000000102';
SQL
echo 'idempotency_00038_concurrent_single_winner=ok'

# A used binder must make Down fail atomically and leave every object intact.
if down38 >/dev/null 2>&1; then echo 'used schema38 Down unexpectedly succeeded' >&2; exit 1; fi
psql_db -Atc "SELECT to_regprocedure('app.bind_idempotency_resource(uuid,uuid,uuid,text,text,text,bytea,bigint,timestamptz,text,uuid)') IS NOT NULL" | grep -qx t
psql_db -Atc "SELECT used FROM app.idempotency_resource_binding_00038_usage" | grep -qx t
echo 'idempotency_00038_used_down_refusal=ok'

sha256sum "$ROOT/internal/middleware/idempotency.go" \
  "$ROOT/internal/platform/idempotencybind/binder.go" \
  "$ROOT/internal/platform/idempotencybind/binder_test.go" \
  "$ROOT/migrations/00037_idempotency_runtime_hardening.sql" \
  "$ROOT/migrations/00038_idempotency_resource_binding.sql" \
  "$ROOT/deploy/configure-app-role.sql" \
  "$ROOT/deploy/test-idempotency-resource-binding-00038-pg18.sh"
echo 'idempotency_resource_binding_00038_pg18_suite=ok'
