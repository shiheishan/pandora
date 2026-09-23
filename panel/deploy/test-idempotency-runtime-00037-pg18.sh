#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
CONTAINER="pandora-idem37-${RUN_ID}"
NETWORK="pandora-idem37-net-${RUN_ID}"
PG_VOLUME="pandora-idem37-pg-${RUN_ID}"
LOG="$(mktemp)"
LOG_CASE="${LOG}.case"
AUDIT_LOG="${PANDORA_TEST_AUDIT_LOG:-}"
VOLUMES_BEFORE="$(mktemp)"
DANGLING_BEFORE="$(mktemp)"
PASSWORD="idempotency-37-test-only"
LABEL_KEY="pandora.idempotency-00037.run"
FIRST_ERROR_LINE=""
BASELINE_QUERY_FAILED=0
OWNED_MOUNT_SOURCES=()
touch "$LOG_CASE"
chmod 0600 "$LOG" "$LOG_CASE" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"

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

  if ! container_names="$(docker ps -a --format '{{.Names}}' 2>/dev/null)"; then
    query_failed=1
  fi
  if ! network_names="$(docker network ls --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
  fi
  if ! volume_names="$(docker volume ls --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
  fi
  if ! label_containers="$(docker ps -a --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Names}}' 2>/dev/null)"; then
    query_failed=1
  fi
  if ! label_networks="$(docker network ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
  fi
  if ! label_volumes="$(docker volume ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)"; then
    query_failed=1
  fi
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
  if ! printf '%s\n' "$volume_names" | awk 'NF' | sort >"$LOG.all.after"; then
    query_failed=1
  fi
  if ! docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$LOG.dangling.after"; then
    query_failed=1
  fi
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
        "$anonymous_new" -ne 0 ]]; then
    cleanup_rc=1
  fi
  printf 'cleanup_query_failed=%s exact_name_container=%s exact_name_network=%s exact_name_volume=%s exact_label_residual=%s mount_source_residual=%s\n' \
    "$query_failed" "$container_remaining" "$network_remaining" "$volume_remaining" \
    "$label_remaining" "$mount_sources_remaining" >&3
  printf 'global_volume_delta_all=%s global_volume_delta_dangling=%s anonymous_volume_new=%s\n' \
    "$global_delta_all" "$global_delta_dangling" "$anonymous_new" >&3
  if grep -Fq "$PASSWORD" "$LOG"; then
    echo 'secret scan failed' >&4; cleanup_rc=1
  fi
  grep -E '^[a-z0-9_]+=ok$' "$LOG" | sort -u >&3 || true
  printf 'audit_log_sha256=%s\n' "$(sha256sum "$LOG" | awk '{print $1}')" >&3
  [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error_line=%s\n' "$FIRST_ERROR_LINE" >&4
  if [[ -n "$AUDIT_LOG" ]]; then
    install -m 0600 "$LOG" "$AUDIT_LOG" || cleanup_rc=1
    printf 'audit_log_path=%s\n' "$AUDIT_LOG" >&4
  fi
  rm -f "$LOG" "$LOG_CASE" "$LOG.all.after" "$LOG.dangling.after" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"
  if [[ "$main_rc" -eq 0 && "$cleanup_rc" -ne 0 ]]; then main_rc="$cleanup_rc"; fi
  exit "$main_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Capture the complete run in one mode-0600 audit log. Only strict marker
# lines, hashes and cleanup totals are emitted to the caller at EXIT.
exec 3>&1 4>&2
exec >>"$LOG" 2>&1

if ! docker volume ls --format '{{.Name}}' 2>/dev/null | sort >"$VOLUMES_BEFORE"; then
  BASELINE_QUERY_FAILED=1
  echo 'failed to query baseline Docker volumes' >&2
  exit 1
fi
if ! docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$DANGLING_BEFORE"; then
  BASELINE_QUERY_FAILED=1
  echo 'failed to query baseline dangling Docker volumes' >&2
  exit 1
fi

IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$IMAGE")"
[[ "$(docker image inspect -f '{{json .Config.Volumes}}' "$IMAGE_ID")" == '{"/var/lib/postgresql":{}}' ]] || {
  echo 'unexpected postgres image volume contract' >&2; exit 1;
}
docker network create --label "$LABEL_KEY=$RUN_ID" "$NETWORK" >/dev/null
docker volume create --label "$LABEL_KEY=$RUN_ID" "$PG_VOLUME" >/dev/null
docker create --name "$CONTAINER" --network "$NETWORK" \
  --label "$LABEL_KEY=$RUN_ID" \
  -e POSTGRES_PASSWORD="$PASSWORD" -e POSTGRES_DB=idem37 \
  --mount "type=volume,src=$PG_VOLUME,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" \
  "$IMAGE_ID" >/dev/null

if ! mount_rows="$(docker inspect -f '{{range .Mounts}}{{println .Type "|" .Name "|" .Source "|" .Destination "|" .RW}}{{end}}' "$CONTAINER" 2>/dev/null)"; then
  echo 'failed to inspect container mounts' >&2
  exit 1
fi
mapfile -t mounts < <(printf '%s\n' "$mount_rows" | sed '/^[[:space:]]*$/d')
[[ "${#mounts[@]}" -eq 2 ]] || { echo 'unexpected container mount count' >&2; exit 1; }
printf '%s\n' "${mounts[@]}" | grep -Fq "volume | $PG_VOLUME | "
printf '%s\n' "${mounts[@]}" | grep -Fq "| /var/lib/postgresql | true"
printf '%s\n' "${mounts[@]}" | grep -Fq "bind |  | $ROOT | /src | false"
volume_mount_source="$(printf '%s\n' "${mounts[@]}" | awk -F'|' -v target="$PG_VOLUME" '
  {for(i=1;i<=NF;i++){gsub(/^[[:space:]]+|[[:space:]]+$/,"",$i)}}
  $1=="volume" && $2==target {print $3}
')"
[[ -n "$volume_mount_source" ]] || { echo 'named volume mount source was not recorded' >&2; exit 1; }
OWNED_MOUNT_SOURCES+=("$volume_mount_source")

if [[ "${PANDORA_TEST_FAILPOINT:-}" == 'after_create' ]]; then
  echo 'intentional failpoint after_create' >&2
  exit 97
fi
if [[ "${PANDORA_TEST_HOLD_AFTER_CREATE:-}" == 'yes' ]]; then
  echo 'holding after_create for signal test'
  while :; do sleep 1; done
fi

docker start "$CONTAINER" >/dev/null
for _ in $(seq 1 60); do
  docker exec "$CONTAINER" pg_isready -U postgres -d idem37 >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$CONTAINER" pg_isready -U postgres -d idem37 >/dev/null

psql_db() {
  local db="$1"; shift
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$db" "$@"
}
psql_app() {
  docker exec -i -e PGPASSWORD="$PASSWORD" "$CONTAINER" \
    psql -X -v ON_ERROR_STOP=1 -h 127.0.0.1 -U aegis_app -d idem37 "$@"
}
up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
down_sql() { awk '/^-- \+goose Down/{down=1;next} down' "$1"; }
apply37() {
  {
    echo 'BEGIN;'
    echo "SET LOCAL app.idempotency_writers_stopped='yes';"
    echo "SET LOCAL app.allow_idempotency_schema37_up='yes';"
    up_sql "$ROOT/migrations/00037_idempotency_runtime_hardening.sql"
    echo 'COMMIT;'
  } | psql_db "$1"
}
apply37_down() {
  {
    echo 'BEGIN;'
    echo "SET LOCAL app.idempotency_writers_stopped='yes';"
    echo "SET LOCAL app.idempotency_probe_callers_stopped='yes';"
    echo "SET LOCAL app.allow_idempotency_schema37_down='yes';"
    down_sql "$ROOT/migrations/00037_idempotency_runtime_hardening.sql"
    echo 'COMMIT;'
  } | psql_db "$1"
}

for migration in "$ROOT"/migrations/000{01..35}_*.sql; do
  up_sql "$migration" | psql_db idem37 >/dev/null
done

psql_db idem37 >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name,default_currency) VALUES
 ('10000000-0000-7000-8000-000000000001','idem37-a','Idem 37 A','CNY'),
 ('20000000-0000-7000-8000-000000000001','idem37-b','Idem 37 B','CNY');
INSERT INTO users(id,tenant_id,email,display_name) VALUES
 ('10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001','a@idem37.test','A'),
 ('10000000-0000-7000-8000-000000000012','10000000-0000-7000-8000-000000000001','a2@idem37.test','A2'),
 ('20000000-0000-7000-8000-000000000011','20000000-0000-7000-8000-000000000001','b@idem37.test','B');

INSERT INTO products(id,tenant_id,code,name) VALUES
 ('10000000-0000-7000-8000-000000000121','10000000-0000-7000-8000-000000000001',
  'idem37-product','Idem 37 Product');
INSERT INTO plans(id,tenant_id,product_id,code,name) VALUES
 ('10000000-0000-7000-8000-000000000131','10000000-0000-7000-8000-000000000001',
  '10000000-0000-7000-8000-000000000121','idem37-plan','Idem 37 Plan');
INSERT INTO orders
 (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
  discount_amount,tax_amount,balance_applied,total_amount,payable_amount,
  paid_amount,expires_at,paid_at,fulfilled_at)
VALUES
 ('10000000-0000-7000-8000-000000000141','10000000-0000-7000-8000-000000000001',
  'IDEM37-LEGACY-NULL','10000000-0000-7000-8000-000000000011','renewal',
  'fulfilled','CNY',100,0,0,0,100,100,100,now()-interval '1 hour',
  now()-interval '2 hours',now()-interval '90 minutes');
INSERT INTO order_items
 (tenant_id,order_id,product_id,plan_id,snapshot_product_name,snapshot_plan_name,
  snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency)
VALUES
 ('10000000-0000-7000-8000-000000000001','10000000-0000-7000-8000-000000000141',
  '10000000-0000-7000-8000-000000000121','10000000-0000-7000-8000-000000000131',
 'Idem 37 Product','Idem 37 Plan','month',1,1,100,100,'CNY');

-- Schema-35 refund row: schema 36 backfills a raw refund_create key and
-- request. Schema 37 must keep that raw legacy binding operational even
-- though actor-scoped idempotency RLS deliberately hides the raw key.
INSERT INTO payment_providers(id,tenant_id,code,adapter,display_name,enabled)
VALUES('10000000-0000-7000-8000-000000000151',
       '10000000-0000-7000-8000-000000000001','idem37-refund','test',
       'Idem 37 Refund',true);
INSERT INTO payment_intents
 (id,tenant_id,order_id,provider_id,currency,amount,status,provider_ref)
VALUES('10000000-0000-7000-8000-000000000152',
       '10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000141',
       '10000000-0000-7000-8000-000000000151','CNY',100,'succeeded',
       'idem37-refund');
INSERT INTO payments
 (id,tenant_id,order_id,payment_intent_id,provider_id,provider_payment_id,
  currency,amount,status)
VALUES('10000000-0000-7000-8000-000000000153',
       '10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000141',
       '10000000-0000-7000-8000-000000000152',
       '10000000-0000-7000-8000-000000000151','idem37-refund-payment',
       'CNY',100,'succeeded');
INSERT INTO refunds
 (id,tenant_id,order_id,payment_id,currency,amount,reason,status,requested_by)
VALUES('10000000-0000-7000-8000-000000000154',
       '10000000-0000-7000-8000-000000000001',
       '10000000-0000-7000-8000-000000000141',
       '10000000-0000-7000-8000-000000000153','CNY',10,
       'raw legacy refund','pending','10000000-0000-7000-8000-000000000011');

INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,response_code,response_body,
  locked_until,completed_at,created_at,expires_at)
VALUES
 ('10000000-0000-7000-8000-000000000101','10000000-0000-7000-8000-000000000001',
  'legacy_json','legacy-envelope',decode(md5('legacy-envelope'),'hex'),'succeeded',200,
  '{"ordinary":true,"ordered":[1,2,3]}',NULL,
  '2026-07-02 00:00:00+00','2026-07-01 00:00:00+00','2026-08-01 00:00:00+00'),
 ('10000000-0000-7000-8000-000000000102','10000000-0000-7000-8000-000000000001',
  'legacy_empty','legacy-empty',decode(md5('legacy-empty'),'hex'),'succeeded',204,
  NULL,NULL,'2026-07-02 00:00:00+00','2026-07-01 00:00:00+00','2026-08-01 00:00:00+00'),
 ('10000000-0000-7000-8000-000000000103','10000000-0000-7000-8000-000000000001',
  'legacy_inflight','legacy-inflight',decode(md5('legacy-inflight'),'hex'),'in_flight',NULL,
  NULL,'2026-07-02 00:00:00+00',NULL,'2026-07-01 00:00:00+00','2026-08-01 00:00:00+00');
SQL

up_sql "$ROOT/migrations/00036_order_reservations.sql" | psql_db idem37 >/dev/null

psql_db idem37 >/dev/null <<'SQL'
INSERT INTO idempotency_keys
 (id,tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('10000000-0000-7000-8000-000000000111','10000000-0000-7000-8000-000000000001',
  'order_create:actor:'||substr(encode(digest('10000000-0000-7000-8000-000000000011','sha256'),'hex'),1,24),
  'actor-a-existing',decode(md5('actor-a-existing'),'hex'),'in_flight',
  '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes'),
 ('10000000-0000-7000-8000-000000000112','10000000-0000-7000-8000-000000000001',
  'order_create:actor:'||substr(encode(digest('10000000-0000-7000-8000-000000000012','sha256'),'hex'),1,24),
  'actor-a2-existing',decode(md5('actor-a2-existing'),'hex'),'in_flight',
  '10000000-0000-7000-8000-000000000012',now()+interval '5 minutes'),
 ('20000000-0000-7000-8000-000000000111','20000000-0000-7000-8000-000000000001',
  'order_create:actor:'||substr(encode(digest('20000000-0000-7000-8000-000000000011','sha256'),'hex'),1,24),
  'actor-b-existing',decode(md5('actor-b-existing'),'hex'),'in_flight',
  '20000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
SQL

apply37 idem37 >/dev/null

psql_db idem37 <<'SQL'
DO $$
BEGIN
  IF (SELECT response_format FROM idempotency_keys WHERE id='10000000-0000-7000-8000-000000000101')<>'legacy_json'
     OR (SELECT response_body->'ordinary' FROM idempotency_keys WHERE id='10000000-0000-7000-8000-000000000101')<>'true'::jsonb
     OR (SELECT response_format FROM idempotency_keys WHERE id='10000000-0000-7000-8000-000000000102')<>'legacy_empty'
     OR (SELECT response_format FROM idempotency_keys WHERE id='10000000-0000-7000-8000-000000000103')<>'none'
     OR EXISTS (SELECT 1 FROM idempotency_keys WHERE claim_generation<>1)
     OR EXISTS (SELECT 1 FROM orders
                 WHERE id='10000000-0000-7000-8000-000000000141'
                   AND (idempotency_key_id IS NOT NULL OR business_request_id IS NULL)) THEN
    RAISE EXCEPTION 'legacy format/generation backfill failed';
  END IF;
END $$;
SELECT 'idempotency_00037_legacy_backfill=ok';

SET ROLE aegis_app;
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','10000000-0000-7000-8000-000000000011',false);
DO $$
DECLARE v_seen int; v_key uuid; v_rows int; v_caught boolean;
BEGIN
  SELECT count(*) INTO v_seen FROM idempotency_keys;
  IF v_seen<>1 THEN RAISE EXCEPTION 'actor RLS expected 1 visible existing row, got %',v_seen; END IF;
  IF (SELECT count(*) FROM app.lookup_legacy_idempotency_key(
       '10000000-0000-7000-8000-000000000001','legacy_json','legacy-envelope'))<>1 THEN
    RAISE EXCEPTION 'same-tenant legacy probe failed';
  END IF;
  v_caught:=false;
  BEGIN
    PERFORM * FROM app.lookup_legacy_idempotency_key(
      '20000000-0000-7000-8000-000000000001','legacy_json','legacy-envelope');
  EXCEPTION WHEN insufficient_privilege THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'cross-tenant legacy probe was allowed'; END IF;

  INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  VALUES('10000000-0000-7000-8000-000000000001',
         app.idempotency_actor_scope('checkout_create','10000000-0000-7000-8000-000000000011'),
         'bytes',decode(md5('bytes'),'hex'),'10000000-0000-7000-8000-000000000011',
         now()+interval '5 minutes') RETURNING id INTO v_key;
  UPDATE idempotency_keys SET status='succeeded',response_code=201,response_format='bytes',
         response_payload=decode('00ff7b7d','hex'),response_content_type='application/octet-stream',
         response_location='/orders/one',response_etag='"one"',
         response_cache_control='private, no-store',response_content_language='zh-CN',
         completed_at=now(),locked_until=NULL WHERE id=v_key;
  IF (SELECT encode(response_payload,'hex') FROM idempotency_keys WHERE id=v_key)<>'00ff7b7d' THEN
    RAISE EXCEPTION 'byte replay was not exact';
  END IF;

  INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  VALUES('10000000-0000-7000-8000-000000000001',
         app.idempotency_actor_scope('checkout_create','10000000-0000-7000-8000-000000000011'),
         'empty-bytes',decode(md5('empty-bytes'),'hex'),'10000000-0000-7000-8000-000000000011',
         now()+interval '5 minutes') RETURNING id INTO v_key;
  UPDATE idempotency_keys SET status='succeeded',response_code=204,response_format='bytes',
         response_payload=decode('','hex'),completed_at=now(),locked_until=NULL WHERE id=v_key;
  IF (SELECT octet_length(response_payload) FROM idempotency_keys WHERE id=v_key)<>0 THEN
    RAISE EXCEPTION 'true empty byte replay failed';
  END IF;

  INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  VALUES('10000000-0000-7000-8000-000000000001',
         app.idempotency_actor_scope('checkout_create','10000000-0000-7000-8000-000000000011'),
         'retry',decode(md5('retry'),'hex'),'10000000-0000-7000-8000-000000000011',
         now()+interval '5 minutes') RETURNING id INTO v_key;
  UPDATE idempotency_keys SET status='failed',response_code=503,response_format='unavailable',
         completed_at=now(),locked_until=NULL WHERE id=v_key;
  UPDATE idempotency_keys SET status='in_flight',claim_generation=2,response_code=NULL,
         response_format='none',completed_at=NULL,locked_until=now()+interval '5 minutes'
   WHERE id=v_key;
  UPDATE idempotency_keys SET status='succeeded',response_code=200,response_format='bytes',
         response_payload=decode('6f6b','hex'),completed_at=now(),locked_until=NULL
   WHERE id=v_key AND claim_generation=1;
  GET DIAGNOSTICS v_rows=ROW_COUNT;
  IF v_rows<>0 THEN RAISE EXCEPTION 'stale generation completed a claim'; END IF;

  v_caught:=false;
  BEGIN
    INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,
                                 actor_id,locked_until)
    VALUES('10000000-0000-7000-8000-000000000001',
           app.idempotency_actor_scope('checkout_create','10000000-0000-7000-8000-000000000011'),
           'forged',decode(md5('forged'),'hex'),'succeeded',
           '10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
  EXCEPTION WHEN insufficient_privilege OR check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'forged terminal insert was accepted'; END IF;
END $$;
RESET ROLE;
SELECT 'idempotency_00037_actor_rls_probe_generation_bytes=ok';
SQL

# Re-running the role bootstrap must not revive response_body writes and must
# preserve exact helper/probe access after its legacy blanket function grant.
docker exec -i -e PGPASSWORD="$PASSWORD" -e AEGIS_DB_APP_PASSWORD="$PASSWORD" "$CONTAINER" \
  psql -X -v ON_ERROR_STOP=1 -U postgres -d idem37 -f /src/deploy/configure-app-role.sql >/dev/null
psql_db idem37 -At <<'SQL' | grep -qx 't|f|t|t|t|f'
SELECT has_column_privilege('aegis_app','idempotency_keys','claim_generation','UPDATE'),
       has_column_privilege('aegis_app','idempotency_keys','response_body','UPDATE'),
       has_function_privilege('aegis_app','app.idempotency_actor_scope(text,uuid)','EXECUTE'),
       has_function_privilege('aegis_app','app.idempotency_scope_matches_actor(text,uuid)','EXECUTE'),
       has_function_privilege('aegis_app','app.lookup_legacy_idempotency_key(uuid,text,text)','EXECUTE'),
       has_function_privilege('public','app.lookup_legacy_idempotency_key(uuid,text,text)','EXECUTE');
SQL
echo 'idempotency_00037_bootstrap_acl=ok'

# Exercise raw schema-36 refund evidence through a real TCP login as aegis_app.
# The narrow grants are disposable test-only transition grants and are revoked
# immediately after the matrix; production configure-app-role remains frozen.
psql_db idem37 -c 'GRANT UPDATE(status) ON refund_requests,refunds TO aegis_app' >/dev/null
psql_db idem37 -c 'GRANT UPDATE(status) ON payments TO aegis_app' >/dev/null
psql_app >/dev/null <<'SQL'
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','10000000-0000-7000-8000-000000000011',false);
DO $$
BEGIN
  IF session_user<>'aegis_app' OR current_user<>'aegis_app' THEN
    RAISE EXCEPTION 'refund runtime test did not use a real aegis_app login';
  END IF;
  IF EXISTS (SELECT 1 FROM idempotency_keys
              WHERE id='10000000-0000-7000-8000-000000000154') THEN
    RAISE EXCEPTION 'raw legacy refund key leaked through actor RLS';
  END IF;
END $$;
BEGIN;
UPDATE refund_requests SET status='processing'
 WHERE id='10000000-0000-7000-8000-000000000154';
UPDATE refunds SET status='processing'
 WHERE id='10000000-0000-7000-8000-000000000154';
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
echo 'idempotency_00037_raw_refund_aegis_app_positive=ok'

: >"$LOG_CASE"
if psql_app -v VERBOSITY=verbose >"$LOG_CASE" 2>&1 <<'SQL'
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','10000000-0000-7000-8000-000000000012',false);
BEGIN;
UPDATE refund_requests SET status=status
 WHERE id='10000000-0000-7000-8000-000000000154';
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
then
  cat "$LOG_CASE" >>"$LOG"
  echo 'wrong actor changed raw legacy refund through aegis_app' >&2
  exit 1
fi
cat "$LOG_CASE" >>"$LOG"
grep -Eq '^ERROR:[[:space:]]+42501:' "$LOG_CASE"
grep -Fq 'refund invariant actor context mismatch' "$LOG_CASE"
echo 'idempotency_00037_raw_refund_wrong_actor=ok'

: >"$LOG_CASE"
if psql_app -v VERBOSITY=verbose >"$LOG_CASE" 2>&1 <<'SQL'
SELECT set_config('app.tenant_id','10000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','',false);
BEGIN;
UPDATE refund_requests SET status=status
 WHERE id='10000000-0000-7000-8000-000000000154';
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
then
  cat "$LOG_CASE" >>"$LOG"
  echo 'missing actor changed raw legacy refund through aegis_app' >&2
  exit 1
fi
cat "$LOG_CASE" >>"$LOG"
grep -Eq '^ERROR:[[:space:]]+42501:' "$LOG_CASE"
grep -Fq 'refund invariant actor context mismatch' "$LOG_CASE"
echo 'idempotency_00037_raw_refund_missing_actor=ok'

psql_app >/dev/null <<'SQL'
SELECT set_config('app.tenant_id','20000000-0000-7000-8000-000000000001',false),
       set_config('app.actor_id','20000000-0000-7000-8000-000000000011',false);
DO $$
DECLARE v_rows int;
BEGIN
  IF EXISTS (SELECT 1 FROM refund_requests
              WHERE id='10000000-0000-7000-8000-000000000154') THEN
    RAISE EXCEPTION 'cross-tenant refund request was visible';
  END IF;
  UPDATE refund_requests SET status=status
   WHERE id='10000000-0000-7000-8000-000000000154';
  GET DIAGNOSTICS v_rows=ROW_COUNT;
  IF v_rows<>0 THEN RAISE EXCEPTION 'cross-tenant refund update affected % rows',v_rows; END IF;
END $$;
SQL
echo 'idempotency_00037_raw_refund_wrong_tenant=ok'
psql_db idem37 -c 'REVOKE UPDATE(status) ON refund_requests,refunds FROM aegis_app' >/dev/null
psql_db idem37 -c 'REVOKE UPDATE(status) ON payments FROM aegis_app' >/dev/null

# Invalid headers, locations and payload boundary must fail closed.
psql_db idem37 >/dev/null <<'SQL'
DO $$
DECLARE v_key uuid; v_caught boolean;
BEGIN
  INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  VALUES('10000000-0000-7000-8000-000000000001',
         app.idempotency_actor_scope('admin_test','10000000-0000-7000-8000-000000000011'),
         'unsafe',decode(md5('unsafe'),'hex'),'10000000-0000-7000-8000-000000000011',
         now()+interval '5 minutes') RETURNING id INTO v_key;
  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET status='succeeded',response_code=200,response_format='bytes',
      response_payload=decode('','hex'),response_location='//evil.test',completed_at=now(),locked_until=NULL
     WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'external location accepted'; END IF;
  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET status='succeeded',response_code=200,response_format='bytes',
      response_payload=decode('','hex'),response_etag=E'bad\nvalue',completed_at=now(),locked_until=NULL
     WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'control-character header accepted'; END IF;
  v_caught:=false;
  BEGIN
    UPDATE idempotency_keys SET status='succeeded',response_code=200,response_format='bytes',
      response_payload=decode(repeat('00',1048577),'hex'),completed_at=now(),locked_until=NULL
     WHERE id=v_key;
  EXCEPTION WHEN check_violation THEN v_caught:=true;
  END;
  IF NOT v_caught THEN RAISE EXCEPTION 'oversize payload accepted'; END IF;
END $$;
SQL
echo 'idempotency_00037_evidence_rejections=ok'

# Real deferred order/claim matrix. The helper exists only inside this
# disposable database and creates the complete schema-36 reservation graph.
psql_db idem37 >/dev/null <<'SQL'
CREATE FUNCTION app.test_idem37_order_fixture(
  p_key uuid,p_order uuid,p_kind text,p_scope text,
  p_claim_tenant uuid,p_claim_actor uuid,p_order_tenant uuid,p_order_user uuid,
  p_resource_type text,p_resource_id uuid,p_sequence text
) RETURNS void LANGUAGE plpgsql SET search_path=pg_catalog AS $$
DECLARE v_reservation uuid:=uuidv7();
BEGIN
  INSERT INTO public.idempotency_keys
    (id,tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
  VALUES(p_key,p_claim_tenant,app.idempotency_actor_scope(p_scope,p_claim_actor),
         'order-matrix-'||p_key::text,public.digest(p_key::text,'sha256'),
         p_claim_actor,now()+interval '5 minutes');
  IF p_sequence='claim_first' THEN
    UPDATE public.idempotency_keys
       SET resource_type=p_resource_type,resource_id=p_resource_id WHERE id=p_key;
  END IF;
  INSERT INTO public.orders
    (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,
     discount_amount,tax_amount,total_amount,balance_applied,payable_amount,
     business_request_id,idempotency_key_id,expires_at)
  VALUES(p_order,p_order_tenant,'IDEM37-'||p_order::text,p_order_user,p_kind,'draft',
         'CNY',100,0,0,100,0,100,p_key,p_key,now()+interval '30 minutes');
  IF p_kind<>'topup' THEN
    INSERT INTO public.order_items
      (tenant_id,order_id,product_id,plan_id,snapshot_product_name,snapshot_plan_name,
       snapshot_interval,snapshot_interval_count,quantity,unit_amount,line_amount,currency)
    VALUES(p_order_tenant,p_order,'10000000-0000-7000-8000-000000000121',
           '10000000-0000-7000-8000-000000000131','Idem 37 Product',
           'Idem 37 Plan','month',1,1,100,100,'CNY');
  END IF;
  INSERT INTO public.order_reservations
    (id,tenant_id,order_id,user_id,state,expires_at)
  VALUES(v_reservation,p_order_tenant,p_order,p_order_user,'held',now()+interval '30 minutes');
  INSERT INTO public.order_reservation_events
    (tenant_id,reservation_id,order_id,from_state,to_state,event_kind,
     business_request_id,actor_kind,actor_id)
  VALUES(p_order_tenant,v_reservation,p_order,NULL,'held','reserve',p_key,'user',p_order_user);
  IF p_kind='new' THEN
    INSERT INTO public.order_stock_reservations
      (tenant_id,reservation_id,order_id,plan_id,quantity)
    VALUES(p_order_tenant,v_reservation,p_order,
           '10000000-0000-7000-8000-000000000131',1);
    UPDATE public.plans SET stock_reserved=stock_reserved+1
     WHERE tenant_id=p_order_tenant AND id='10000000-0000-7000-8000-000000000131';
  END IF;
  IF p_sequence='order_first' THEN
    UPDATE public.idempotency_keys
       SET resource_type=p_resource_type,resource_id=p_resource_id WHERE id=p_key;
  END IF;
END;
$$;
REVOKE ALL ON FUNCTION app.test_idem37_order_fixture(
  uuid,uuid,text,text,uuid,uuid,uuid,uuid,text,uuid,text) FROM PUBLIC,aegis_app;
SQL

psql_db idem37 >/dev/null <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000201','10000000-0000-7000-8000-000000000211',
 'renewal','subscription_renewal_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000211','order_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000202','10000000-0000-7000-8000-000000000212',
 'new','order_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000212','claim_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
echo 'idempotency_00037_order_first_claim_first=ok'

expect_order_rejected() {
  local marker="$1" expected_sqlstate="$2" classification="$3" evidence="$4"
  local error_count
  : >"$LOG_CASE"
  if psql_db idem37 -v VERBOSITY=verbose >"$LOG_CASE" 2>&1; then
    cat "$LOG_CASE" >>"$LOG"
    echo "expected order/claim rejection: $marker" >&2
    exit 1
  fi
  cat "$LOG_CASE" >>"$LOG"
  error_count="$(awk '/^ERROR:/{n++} END{print n+0}' "$LOG_CASE")"
  if [[ "$error_count" -ne 1 ]] ||
     ! grep -Eq "^ERROR:[[:space:]]+${expected_sqlstate}:" "$LOG_CASE"; then
    echo "wrong SQLSTATE/error count for $marker" >&2
    exit 1
  fi
  case "$classification" in
    invariant)
      grep -Fq 'order/idempotency invariant:' "$LOG_CASE" || {
        echo "missing order/idempotency invariant classification for $marker" >&2; exit 1;
      }
      ;;
    constraint)
      grep -Fq "constraint \"$evidence\"" "$LOG_CASE" || {
        echo "wrong constraint classification for $marker" >&2; exit 1;
      }
      ;;
    *) echo "unknown rejection classification for $marker" >&2; exit 1 ;;
  esac
  echo "$marker=ok"
}

expect_order_rejected idempotency_00037_order_wrong_actor 23503 invariant order_idempotency <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000203','10000000-0000-7000-8000-000000000213',
 'renewal','subscription_renewal_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000012','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000213','order_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_order_wrong_scope 23503 invariant order_idempotency <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000204','10000000-0000-7000-8000-000000000214',
 'renewal','order_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000214','claim_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_order_wrong_resource_type 23503 invariant order_idempotency <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000205','10000000-0000-7000-8000-000000000215',
 'renewal','subscription_renewal_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','subscription',
 '10000000-0000-7000-8000-000000000215','order_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_order_wrong_resource_id 23503 invariant order_idempotency <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000206','10000000-0000-7000-8000-000000000216',
 'renewal','subscription_renewal_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000299','claim_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_order_wrong_tenant 23503 constraint orders_idempotency_key_fk <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '20000000-0000-7000-8000-000000000207','10000000-0000-7000-8000-000000000217',
 'renewal','subscription_renewal_create','20000000-0000-7000-8000-000000000001',
 '20000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000217','claim_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_order_unsupported_kind 23503 invariant order_idempotency <<'SQL'
BEGIN;
SELECT app.test_idem37_order_fixture(
 '10000000-0000-7000-8000-000000000208','10000000-0000-7000-8000-000000000218',
 'topup','balance_topup_create','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','10000000-0000-7000-8000-000000000001',
 '10000000-0000-7000-8000-000000000011','order',
 '10000000-0000-7000-8000-000000000218','order_first');
SET CONSTRAINTS ALL IMMEDIATE;
COMMIT;
SQL
expect_order_rejected idempotency_00037_second_claim_same_order 23505 constraint idempotency_keys_one_order_resource_00037 <<'SQL'
BEGIN;
INSERT INTO idempotency_keys(id,tenant_id,scope,idempotency_key,request_hash,actor_id,locked_until)
VALUES('10000000-0000-7000-8000-000000000209','10000000-0000-7000-8000-000000000001',
 app.idempotency_actor_scope('subscription_renewal_create','10000000-0000-7000-8000-000000000011'),
 'second-claim',digest('second-claim','sha256'),'10000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
UPDATE idempotency_keys SET resource_type='order',resource_id='10000000-0000-7000-8000-000000000211'
 WHERE id='10000000-0000-7000-8000-000000000209';
COMMIT;
SQL
expect_order_rejected idempotency_00037_second_order_same_claim 23505 constraint orders_business_request_unique <<'SQL'
BEGIN;
INSERT INTO orders
 (id,tenant_id,order_no,user_id,kind,status,currency,subtotal_amount,discount_amount,
  tax_amount,total_amount,balance_applied,payable_amount,business_request_id,
  idempotency_key_id,expires_at)
VALUES('10000000-0000-7000-8000-000000000219','10000000-0000-7000-8000-000000000001',
 'IDEM37-SECOND-ORDER','10000000-0000-7000-8000-000000000011','renewal','draft',
 'CNY',100,0,0,100,0,100,'10000000-0000-7000-8000-000000000201',
 '10000000-0000-7000-8000-000000000201',now()+interval '30 minutes');
COMMIT;
SQL
psql_db idem37 -c 'DROP FUNCTION app.test_idem37_order_fixture(uuid,uuid,text,text,uuid,uuid,uuid,uuid,text,uuid,text)' >/dev/null
echo 'idempotency_00037_order_matrix=ok'

# Build a pristine schema-36 database for deterministic preflight refusal tests.
psql_db postgres -c 'CREATE DATABASE idem37_negative' >/dev/null
for migration in "$ROOT"/migrations/000{01..36}_*.sql; do
  up_sql "$migration" | psql_db idem37_negative >/dev/null
done
psql_db idem37_negative >/dev/null <<'SQL'
INSERT INTO tenants(id,slug,display_name) VALUES
 ('30000000-0000-7000-8000-000000000001','negative','Negative');
INSERT INTO users(id,tenant_id,email) VALUES
 ('30000000-0000-7000-8000-000000000011','30000000-0000-7000-8000-000000000001','negative@idem37.test');
SQL

expect_up_rejected() {
  local db="$1" pattern="$2" marker="$3"
  : >"$LOG_CASE"
  if apply37 "$db" >"$LOG_CASE" 2>&1; then
    cat "$LOG_CASE" >>"$LOG"
    echo "expected 00037 Up rejection for $db" >&2; exit 1
  fi
  cat "$LOG_CASE" >>"$LOG"
  grep -q "$pattern" "$LOG_CASE"
  echo "$marker=ok"
}
for kind in malformed mismatch overlap v11_envelope sentinel_collision; do
  db="idem37_${kind}"
  psql_db postgres -c "CREATE DATABASE $db TEMPLATE idem37_negative" >/dev/null
  case "$kind" in
    malformed)
      psql_db "$db" >/dev/null <<'SQL'
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES('30000000-0000-7000-8000-000000000001','order_create:actor:bad','bad',decode(md5('bad'),'hex'),'in_flight','30000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
SQL
      expect_up_rejected "$db" 'malformed reserved actor suffix' idempotency_00037_malformed_preflight ;;
    mismatch)
      psql_db "$db" >/dev/null <<'SQL'
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES('30000000-0000-7000-8000-000000000001','order_create:actor:000000000000000000000000','bad',decode(md5('bad'),'hex'),'in_flight','30000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
SQL
      expect_up_rejected "$db" 'null or mismatched actor digest' idempotency_00037_digest_preflight ;;
    overlap)
      psql_db "$db" >/dev/null <<'SQL'
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES
 ('30000000-0000-7000-8000-000000000001','order_create','same',decode(md5('same'),'hex'),'in_flight',NULL,now()+interval '5 minutes'),
 ('30000000-0000-7000-8000-000000000001','order_create:actor:'||substr(encode(digest('30000000-0000-7000-8000-000000000011','sha256'),'hex'),1,24),'same',decode(md5('same'),'hex'),'in_flight','30000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
SQL
      expect_up_rejected "$db" 'both raw and actor-bound claims' idempotency_00037_overlap_preflight ;;
    v11_envelope)
      psql_db "$db" >/dev/null <<'SQL'
ALTER TABLE idempotency_keys DISABLE TRIGGER zz_idempotency_evidence_guard;
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,response_code,
 response_body,actor_id,locked_until,completed_at)
VALUES('30000000-0000-7000-8000-000000000001','legacy','v11-envelope',
 decode(md5('v11-envelope'),'hex'),'succeeded',201,
 '{"_aegis_envelope":{"version":1,"body":"e30="}}',NULL,NULL,now());
ALTER TABLE idempotency_keys ENABLE TRIGGER zz_idempotency_evidence_guard;
SQL
      expect_up_rejected "$db" 'ambiguous _aegis_envelope JSON evidence' idempotency_00037_v11_envelope_preflight ;;
    sentinel_collision)
      psql_db "$db" >/dev/null <<'SQL'
ALTER TABLE idempotency_keys DISABLE TRIGGER zz_idempotency_evidence_guard;
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,response_code,
 response_body,actor_id,locked_until,completed_at)
VALUES('30000000-0000-7000-8000-000000000001','legacy','business-json-collision',
 decode(md5('business-json-collision'),'hex'),'succeeded',200,
 '{"_aegis_envelope":{"customer_owned":true},"business":"ordinary"}',NULL,NULL,now());
ALTER TABLE idempotency_keys ENABLE TRIGGER zz_idempotency_evidence_guard;
SQL
      expect_up_rejected "$db" 'ambiguous _aegis_envelope JSON evidence' idempotency_00037_sentinel_collision_preflight ;;
  esac
done

# A raw-only untouched database can round-trip exactly with explicit operator
# acknowledgements. Any schema-37 evidence or row drift refuses Down.
psql_db postgres -c 'CREATE DATABASE idem37_clean TEMPLATE idem37_negative' >/dev/null
psql_db idem37_clean >/dev/null <<'SQL'
ALTER TABLE idempotency_keys DISABLE TRIGGER zz_idempotency_evidence_guard;
INSERT INTO idempotency_keys(id,tenant_id,scope,idempotency_key,request_hash,status,
 response_code,response_body,actor_id,locked_until,completed_at,created_at,expires_at)
VALUES('30000000-0000-7000-8000-000000000101','30000000-0000-7000-8000-000000000001',
 'legacy','clean',decode(md5('clean'),'hex'),'succeeded',200,'{"ok":true}',NULL,NULL,
 '2026-07-02 00:00:00+00','2026-07-01 00:00:00+00','2026-08-01 00:00:00+00');
ALTER TABLE idempotency_keys ENABLE TRIGGER zz_idempotency_evidence_guard;
CREATE TABLE expected_idem37_down AS SELECT id,to_jsonb(k) row_data FROM idempotency_keys k;
SQL
apply37 idem37_clean >/dev/null
apply37_down idem37_clean >/dev/null
psql_db idem37_clean -At >"$LOG_CASE" <<'SQL'
SELECT 'columns',NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name='idempotency_keys' AND column_name='claim_generation')
UNION ALL SELECT 'functions',
  to_regprocedure('app.lookup_legacy_idempotency_key(uuid,text,text)') IS NULL
  AND to_regprocedure('app.assert_idempotency_order_binding(uuid,uuid,uuid)') IS NULL
  AND to_regprocedure('app.assert_idempotency_order_binding_trigger()') IS NULL
UNION ALL SELECT 'order_catalog',
  to_regclass('public.idempotency_keys_one_order_resource_00037') IS NULL
  AND NOT EXISTS(SELECT 1 FROM pg_trigger
                  WHERE tgname IN ('trg_orders_idempotency_00037_commit',
                                   'trg_idempotency_orders_00037_commit'))
UNION ALL SELECT 'policy',
  (SELECT array_agg(polname::text ORDER BY polname) FROM pg_policy
    WHERE polrelid='idempotency_keys'::regclass)=ARRAY['tenant_isolation']::text[]
UNION ALL SELECT 'refund_function_flags',
  NOT EXISTS(SELECT 1 FROM pg_proc
              WHERE oid IN ('app.guard_refund_request()'::regprocedure,
                            'app.assert_refund_request(uuid,uuid)'::regprocedure,
                            'app.assert_refund_idempotency_key(uuid,uuid)'::regprocedure,
                            'app.assert_refund_request_trigger()'::regprocedure,
                            'app.assert_refund_idempotency_key_trigger()'::regprocedure)
                AND (prosecdef OR proconfig IS NOT NULL))
UNION ALL SELECT 'acl',
  has_column_privilege('aegis_app','idempotency_keys','status','INSERT')
  AND has_column_privilege('aegis_app','idempotency_keys','response_body','UPDATE')
  AND NOT has_table_privilege('aegis_app','idempotency_keys','DELETE')
UNION ALL SELECT 'rows',
  NOT EXISTS(SELECT 1 FROM idempotency_keys k FULL JOIN expected_idem37_down e USING(id)
              WHERE k.id IS NULL OR e.id IS NULL OR to_jsonb(k)<>e.row_data);
SQL
cat "$LOG_CASE" >>"$LOG"
if grep -Eq '\|f$' "$LOG_CASE"; then
  echo 'clean Down catalog or row restoration failed' >&2
  exit 1
fi
echo 'idempotency_00037_clean_down_exact=ok'

psql_db postgres -c 'CREATE DATABASE idem37_down_refuse TEMPLATE idem37_negative' >/dev/null
apply37 idem37_down_refuse >/dev/null
psql_db idem37_down_refuse >/dev/null <<'SQL'
INSERT INTO idempotency_keys(tenant_id,scope,idempotency_key,request_hash,status,actor_id,locked_until)
VALUES('30000000-0000-7000-8000-000000000001',
 app.idempotency_actor_scope('order_create','30000000-0000-7000-8000-000000000011'),
 'new-writer',decode(md5('new-writer'),'hex'),'in_flight',
 '30000000-0000-7000-8000-000000000011',now()+interval '5 minutes');
SQL
if apply37_down idem37_down_refuse >"$LOG_CASE" 2>&1; then
  cat "$LOG_CASE" >>"$LOG"
  echo 'Down accepted schema-37 actor-bound data' >&2; exit 1
fi
cat "$LOG_CASE" >>"$LOG"
grep -q 'actor-bound scope\|evidence changed after Up' "$LOG_CASE"
echo 'idempotency_00037_down_refusal=ok'

echo 'idempotency_runtime_00037_pg18_suite=ok'
