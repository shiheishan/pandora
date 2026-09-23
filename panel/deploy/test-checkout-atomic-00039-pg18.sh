#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
POSTGRES_IMAGE="${PANDORA_TEST_POSTGRES_IMAGE:-postgres:18}"
GO_IMAGE="${PANDORA_TEST_GO_IMAGE:-golang:1.26}"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$-${RANDOM}"
PG_CONTAINER="pandora-checkout39-pg-${RUN_ID}"
GO_CONTAINER="pandora-checkout39-go-${RUN_ID}"
NETWORK="pandora-checkout39-net-${RUN_ID}"
PG_VOLUME=""
MOD_VOLUME=""
LABEL_KEY="pandora.checkout-00039.run"
DB_NAME="checkout39"
POSTGRES_PASSWORD="checkout39-postgres-test-only"
APP_PASSWORD="checkout39-app-test-only"
DSN="postgres://aegis_app:${APP_PASSWORD}@pg18:5432/${DB_NAME}?sslmode=disable&application_name=pandora-settlement-${RUN_ID}"
AUDIT_LOG_DEST="${PANDORA_TEST_AUDIT_LOG:-}"
LOG="$(mktemp)"
VOLUMES_BEFORE="$(mktemp)"
DANGLING_BEFORE="$(mktemp)"
FIRST_ERROR_LINE=""
BASELINE_QUERY_FAILED=0
BUSINESS_GATE=0
PG_CONTAINER_ID=""
GO_CONTAINER_ID=""
NETWORK_ID=""
PG_VOLUME_ID=""
MOD_VOLUME_ID=""
POSTGRES_IMAGE_ID=""
GO_IMAGE_ID=""
OWNED_MOUNT_SOURCES=()
chmod 0600 "$LOG" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"

early_cleanup() {
  rm -f "$LOG" "$VOLUMES_BEFORE" "$DANGLING_BEFORE"
}
trap 'early_cleanup' EXIT
trap 'early_cleanup; exit 130' INT
trap 'early_cleanup; exit 143' TERM

command -v docker >/dev/null 2>&1 || { echo 'docker is required' >&2; exit 1; }
command -v awk >/dev/null 2>&1 || { echo 'awk is required' >&2; exit 1; }
command -v comm >/dev/null 2>&1 || { echo 'comm is required' >&2; exit 1; }
command -v sha256sum >/dev/null 2>&1 || { echo 'sha256sum is required' >&2; exit 1; }
command -v install >/dev/null 2>&1 || { echo 'install is required' >&2; exit 1; }
command -v timeout >/dev/null 2>&1 || { echo 'timeout is required' >&2; exit 1; }

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
  local containers networks volumes labels_c labels_n labels_v
  local exact_c exact_n exact_v label_remaining mount_remaining=0 anonymous_new=-1
  local source new_all
  trap - EXIT ERR INT TERM
  set +e
  cleanup_container() {
    local name="$1" expected_id="$2" actual
    [[ -n "$expected_id" ]] || return 0
    actual="$(docker inspect -f "{{.Id}}|{{index .Config.Labels \"$LABEL_KEY\"}}" "$expected_id" 2>/dev/null)" || {
      cleanup_rc=1
      return
    }
    [[ "$actual" == "$expected_id|$RUN_ID" ]] || {
      printf 'cleanup_identity_mismatch=container:%s\n' "$name" >&4
      cleanup_rc=1
      return
    }
    docker rm -fv "$expected_id" >/dev/null 2>&1 || cleanup_rc=1
  }
  cleanup_network() {
    local name="$1" expected_id="$2" actual
    [[ -n "$expected_id" ]] || return 0
    actual="$(docker network inspect -f "{{.Id}}|{{index .Labels \"$LABEL_KEY\"}}" "$expected_id" 2>/dev/null)" || {
      cleanup_rc=1
      return
    }
    [[ "$actual" == "$expected_id|$RUN_ID" ]] || {
      printf 'cleanup_identity_mismatch=network:%s\n' "$name" >&4
      cleanup_rc=1
      return
    }
    docker network rm "$expected_id" >/dev/null 2>&1 || cleanup_rc=1
  }
  cleanup_container "$GO_CONTAINER" "$GO_CONTAINER_ID"
  cleanup_container "$PG_CONTAINER" "$PG_CONTAINER_ID"
  cleanup_network "$NETWORK" "$NETWORK_ID"
  containers="$(docker ps -a --format '{{.Names}}' 2>/dev/null)" || query_failed=1
  networks="$(docker network ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  volumes="$(docker volume ls --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  labels_c="$(docker ps -a --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Names}}' 2>/dev/null)" || query_failed=1
  labels_n="$(docker network ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  labels_v="$(docker volume ls --filter "label=$LABEL_KEY=$RUN_ID" --format '{{.Name}}' 2>/dev/null)" || query_failed=1
  exact_c="$(awk -v a="$PG_CONTAINER" -v b="$GO_CONTAINER" '$0==a||$0==b{n++} END{print n+0}' <<<"$containers")"
  exact_n="$(awk -v x="$NETWORK" '$0==x{n++} END{print n+0}' <<<"$networks")"
  exact_v="$(awk -v a="$PG_VOLUME" -v b="$MOD_VOLUME" '$0==a||$0==b{n++} END{print n+0}' <<<"$volumes")"
  label_remaining=$((
    $(awk 'NF{n++} END{print n+0}' <<<"$labels_c") +
    $(awk 'NF{n++} END{print n+0}' <<<"$labels_n") +
    $(awk 'NF{n++} END{print n+0}' <<<"$labels_v")
  ))
  for source in "${OWNED_MOUNT_SOURCES[@]}"; do
    [[ ! -e "$source" ]] || mount_remaining=$((mount_remaining + 1))
  done
  printf '%s\n' "$volumes" | awk 'NF' | sort >"$LOG.all.after" || query_failed=1
  docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$LOG.dangling.after" || query_failed=1
  if [[ "$query_failed" -eq 0 ]]; then
    new_all="$(comm -13 "$VOLUMES_BEFORE" "$LOG.all.after")"
    anonymous_new="$(awk 'length($0)==64 && $0~/^[0-9a-f]+$/{n++} END{print n+0}' <<<"$new_all")"
  fi
  if [[ "$query_failed" -ne 0 || "$exact_c" -ne 0 || "$exact_n" -ne 0 ||
        "$exact_v" -ne 0 || "$label_remaining" -ne 0 || "$mount_remaining" -ne 0 ||
        "$anonymous_new" -ne 0 ]]; then cleanup_rc=1; fi
  printf 'cleanup_query_failed=%s exact_containers=%s exact_network=%s exact_volumes=%s exact_label_residual=%s mount_source_residual=%s anonymous_volume_new=%s\n' \
    "$query_failed" "$exact_c" "$exact_n" "$exact_v" "$label_remaining" \
    "$mount_remaining" "$anonymous_new" >&3
  grep -Fq "$POSTGRES_PASSWORD" "$LOG"
  postgres_secret_rc=$?
  grep -Fq "$APP_PASSWORD" "$LOG"
  app_secret_rc=$?
  if [[ "$postgres_secret_rc" -eq 0 || "$app_secret_rc" -eq 0 ]]; then
    echo 'secret_scan=failed' >&4
    cleanup_rc=1
  elif [[ "$postgres_secret_rc" -eq 1 && "$app_secret_rc" -eq 1 ]]; then
    echo 'secret_scan=ok' >&3
    if [[ -n "$AUDIT_LOG_DEST" ]]; then
      install -m 0600 "$LOG" "$AUDIT_LOG_DEST" || cleanup_rc=1
    fi
  else
    echo 'secret_scan=error' >&4
    cleanup_rc=1
  fi
  grep -E 'marker=(checkout|settlement)_pg18_' "$LOG" >&3 || cleanup_rc=1
  audit_hash="$(sha256sum "$LOG" | awk '{print $1}')" || cleanup_rc=1
  [[ -n "$audit_hash" ]] || cleanup_rc=1
  printf 'audit_log_sha256=%s\n' "$audit_hash" >&3
  [[ -z "$FIRST_ERROR_LINE" ]] || printf 'first_error_line=%s\n' "$FIRST_ERROR_LINE" >&4
  cleanup_files=("$LOG" "$LOG.all.after" "$LOG.dangling.after" "$VOLUMES_BEFORE" "$DANGLING_BEFORE")
  rm -f "${cleanup_files[@]}" || cleanup_rc=1
  for cleanup_file in "${cleanup_files[@]}"; do
    [[ ! -e "$cleanup_file" && ! -L "$cleanup_file" ]] || cleanup_rc=1
  done
  if [[ "$main_rc" -eq 0 && "$cleanup_rc" -eq 0 && "$BUSINESS_GATE" -eq 1 ]]; then
    echo 'settlement_pg18_full_suite=ok business=ok cleanup=ok schema=40' >&3
    echo 'checkout_atomic_pg18_full_suite=ok business=ok cleanup=ok schema=40' >&3
  elif [[ "$main_rc" -eq 0 ]]; then
    main_rc=1
  fi
  exit "$main_rc"
}
trap 'cleanup $?' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

exec 3>&1 4>&2
exec >>"$LOG" 2>&1
docker info >/dev/null
docker volume ls --format '{{.Name}}' 2>/dev/null | sort >"$VOLUMES_BEFORE" || BASELINE_QUERY_FAILED=1
docker volume ls --filter dangling=true --format '{{.Name}}' 2>/dev/null | sort >"$DANGLING_BEFORE" || BASELINE_QUERY_FAILED=1
[[ "$BASELINE_QUERY_FAILED" -eq 0 ]]

up_sql() { awk '/^-- \+goose Down/{exit} /^-- \+goose Up/{up=1;next} up' "$1"; }
psql_admin() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" "$PG_CONTAINER_ID" \
    psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" "$@"
}
configure_role() {
  docker exec -i -e PGPASSWORD="$POSTGRES_PASSWORD" -e AEGIS_DB_APP_PASSWORD="$APP_PASSWORD" \
    "$PG_CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d "$DB_NAME" \
    -f /src/deploy/configure-app-role.sql >/dev/null
}
apply_guarded() {
  local number="$1" name="$2"
  { echo 'BEGIN;'; echo "SET LOCAL app.idempotency_writers_stopped='yes';";
    echo "SET LOCAL app.allow_idempotency_schema${number}_up='yes';";
    up_sql "$ROOT/migrations/$name"; echo 'COMMIT;'; } | psql_admin >/dev/null
}

! docker inspect "$PG_CONTAINER" >/dev/null 2>&1
! docker inspect "$GO_CONTAINER" >/dev/null 2>&1
! docker network inspect "$NETWORK" >/dev/null 2>&1
POSTGRES_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$POSTGRES_IMAGE")"
GO_IMAGE_ID="$(docker image inspect -f '{{.Id}}' "$GO_IMAGE")"
[[ -n "$POSTGRES_IMAGE_ID" && -n "$GO_IMAGE_ID" ]]

docker network create --label "$LABEL_KEY=$RUN_ID" "$NETWORK" >/dev/null
NETWORK_ID="$(docker network inspect -f '{{.Id}}' "$NETWORK")"
docker create --name "$PG_CONTAINER" --network "$NETWORK_ID" --network-alias pg18 \
  --label "$LABEL_KEY=$RUN_ID" -e POSTGRES_PASSWORD="$POSTGRES_PASSWORD" -e POSTGRES_DB="$DB_NAME" \
  --mount "type=volume,dst=/var/lib/postgresql" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" "$POSTGRES_IMAGE" >/dev/null
PG_CONTAINER_ID="$(docker inspect -f '{{.Id}}' "$PG_CONTAINER")"
PG_VOLUME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql"}}{{.Name}}{{end}}{{end}}' "$PG_CONTAINER_ID")"
PG_VOLUME_ID="$PG_VOLUME"
[[ -n "$PG_VOLUME" ]]
docker create --name "$GO_CONTAINER" --network "$NETWORK_ID" --label "$LABEL_KEY=$RUN_ID" \
  --mount "type=bind,src=$ROOT,dst=/src,readonly" \
  --mount "type=volume,dst=/go/pkg/mod" -w /src \
	-e "AEGIS_CHECKOUT_PG18_DSN=$DSN" \
	-e "AEGIS_SETTLEMENT_PG18_DSN=$DSN" \
	-e "AEGIS_SETTLEMENT_PG18_EXPECT_RUN_ID=$RUN_ID" \
	-e "AEGIS_SETTLEMENT_PG18_EXPECT_DB_NAME=$DB_NAME" "$GO_IMAGE" sh -ec '
	  case "$(go env GOVERSION)" in go1.26.*) ;; *) echo "Go 1.26 is required" >&2; exit 1;; esac
	  go version
	  go test -v -count=1 -timeout=150s \
	    -run "^(TestCheckoutAtomicPG18|TestSettlementPG18)$" ./internal/domain/billing
	' >/dev/null
GO_CONTAINER_ID="$(docker inspect -f '{{.Id}}' "$GO_CONTAINER")"
MOD_VOLUME="$(docker inspect -f '{{range .Mounts}}{{if eq .Destination "/go/pkg/mod"}}{{.Name}}{{end}}{{end}}' "$GO_CONTAINER_ID")"
MOD_VOLUME_ID="$MOD_VOLUME"
[[ -n "$MOD_VOLUME" && "$MOD_VOLUME" != "$PG_VOLUME" ]]

for container in "$PG_CONTAINER_ID" "$GO_CONTAINER_ID"; do
  [[ "$(docker inspect -f "{{index .Config.Labels \"$LABEL_KEY\"}}" "$container")" == "$RUN_ID" ]]
  while IFS='|' read -r type name source destination rw; do
    [[ -n "$type" ]] || continue
    [[ "$type" != volume ]] || OWNED_MOUNT_SOURCES+=("$source")
  done < <(docker inspect -f '{{range .Mounts}}{{printf "%s|%s|%s|%s|%t\n" .Type .Name .Source .Destination .RW}}{{end}}' "$container")
done
[[ "$(docker network inspect -f "{{index .Labels \"$LABEL_KEY\"}}" "$NETWORK")" == "$RUN_ID" ]]
[[ "$(docker volume inspect -f '{{.Name}}' "$PG_VOLUME_ID")" == "$PG_VOLUME" ]]
[[ "$(docker volume inspect -f '{{.Name}}' "$MOD_VOLUME_ID")" == "$MOD_VOLUME" ]]
[[ "$(docker inspect -f '{{.Image}}' "$PG_CONTAINER_ID")" == "$POSTGRES_IMAGE_ID" ]]
[[ "$(docker inspect -f '{{.Image}}' "$GO_CONTAINER_ID")" == "$GO_IMAGE_ID" ]]
echo "marker=checkout_pg18_image_identity_ok postgres=$POSTGRES_IMAGE_ID go=$GO_IMAGE_ID"
[[ "$(docker image inspect -f '{{if index .Config.Volumes "/var/lib/postgresql"}}yes{{else}}no{{end}}' "$POSTGRES_IMAGE_ID")" == yes ]]
[[ "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER_ID")" == '{}' ||
   "$(docker inspect -f '{{json .HostConfig.PortBindings}}' "$PG_CONTAINER_ID")" == 'null' ]]

docker start "$PG_CONTAINER_ID" >/dev/null
# 等 initdb 完成。
#
# 原本是 90 秒，在一台只有 1 vCPU、同时还跑着生产库的机器上不够——
# 同一份脚本连跑两次，一次过一次超时。超时的表现是后面那句 pg_isready
# 断言失败，看不出是「没起来」还是「起得慢」，很容易误判成容器崩了。
: "${CHECKOUT_PG18_READY_TIMEOUT:=300}"
for _ in $(seq 1 "$CHECKOUT_PG18_READY_TIMEOUT"); do
  docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null 2>&1 && break
  sleep 1
done
docker exec "$PG_CONTAINER_ID" pg_isready -U postgres -d "$DB_NAME" >/dev/null
pg_version_num="$(psql_admin -Atqc 'SHOW server_version_num')"
[[ "$pg_version_num" =~ ^[0-9]+$ && "$pg_version_num" -ge 180000 && "$pg_version_num" -lt 190000 ]]
echo "marker=checkout_pg18_postgres_version_ok version_num=$pg_version_num"

count=0
for migration in "$ROOT"/migrations/000{01..36}_*.sql; do
	[[ -f "$migration" ]]
	{ echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | psql_admin >/dev/null
  count=$((count + 1))
done
[[ "$count" -eq 36 ]]
configure_role
apply_guarded 37 00037_idempotency_runtime_hardening.sql
configure_role
apply_guarded 38 00038_idempotency_resource_binding.sql
configure_role
apply_guarded 39 00039_bound_idempotency_success.sql
configure_role
{ echo 'BEGIN;'; up_sql "$ROOT/migrations/00040_order_release_and_late_suspense.sql"; echo 'COMMIT;'; } | psql_admin >/dev/null

# 40 之后的迁移也要跑。
#
# 这个 gate 原本停在 schema 40——它是那个版本的产物。但被它验证的代码一直在
# 往前走：settlement 现在会写 plugin_hook_deliveries（40 之后才建的表），
# 而 orders 的列级 INSERT 授权在 00060 才补齐。停在 40 的库跑新代码，得到的
# 是「relation does not exist」和「permission denied」这类看着像环境问题、
# 实际是版本落后的报错。
#
# 用字符串比较而不是写死上界：零填充的编号可以直接比大小，将来加新迁移
# 不用回来改这里——这个 gate 落后于代码，起因正是它把范围写死了。
for migration in "$ROOT"/migrations/*.sql; do
  base="$(basename "$migration")"
  [[ "${base%%_*}" > "00040" ]] || continue
  { echo 'BEGIN;'; up_sql "$migration"; echo 'COMMIT;'; } | psql_admin >/dev/null
done
configure_role
echo 'marker=checkout_pg18_real_migrations_1_40_configured_ok'

# seed 已抽到 deploy/fixtures/billing-pg18-seed.sql，与 run-pg18-gates.sh 共用。
# 两处各留一份的话，迟早会像 orders 的列级授权清单那样改了一处忘了另一处。
psql_admin >/dev/null < "$ROOT/deploy/fixtures/billing-pg18-seed.sql"
echo 'marker=checkout_pg18_fixture_ok'
echo 'marker=settlement_pg18_fixture_ok'

timeout --signal=TERM --kill-after=15s 210s docker start -a "$GO_CONTAINER_ID"
grep -Eq '^=== RUN[[:space:]]+TestCheckoutAtomicPG18$' "$LOG"
grep -Eq '^--- PASS: TestCheckoutAtomicPG18 ' "$LOG"
grep -Eq '^=== RUN[[:space:]]+TestSettlementPG18$' "$LOG"
grep -Eq '^--- PASS: TestSettlementPG18 ' "$LOG"
required_markers=(
  checkout_pg18_postgres_version_ok
  checkout_pg18_image_identity_ok
  checkout_pg18_real_migrations_1_40_configured_ok
  checkout_pg18_fixture_ok
  checkout_pg18_case_a_pending_exact_bytes_ok
  checkout_pg18_case_b_captured_fulfilled_ledger_ok
  checkout_pg18_case_c_full_rollback_ok
  checkout_pg18_case_d_terminal_key_graph_singular_ok
  settlement_pg18_fixture_ok
  settlement_pg18_target_identity_ok
  settlement_pg18_runtime_acl_rls_ok
  settlement_pg18_external_full_ok
  settlement_pg18_duplicate_identity_ok
  settlement_pg18_100_exact_event_ok
  settlement_pg18_100_same_payment_ok
  settlement_pg18_mixed_hold_fee_ok
  settlement_pg18_topup_parent_only_ok
  settlement_pg18_commission_success_shared_locks_ok
  settlement_pg18_commission_conflict_rollback_ok
  settlement_pg18_fault_after_ledger_retry_ok
  settlement_pg18_fault_after_capture_retry_ok
  settlement_pg18_fault_after_fulfil_retry_ok
  settlement_pg18_fault_after_audit_retry_ok
  settlement_pg18_fault_matrix_retry_ok
  settlement_pg18_wrong_amount_rollback_ok
  settlement_pg18_wrong_currency_rollback_ok
  settlement_pg18_wrong_payment_rollback_ok
  settlement_pg18_legacy_renewal_fail_closed_ok
  settlement_pg18_actor_bound_renewal_ok
  settlement_pg18_terminal_quarantine_exactly_once_ok
  settlement_pg18_corrupt_missing_child_fail_closed_ok
  settlement_pg18_corrupt_amount_mismatch_fail_closed_ok
  settlement_pg18_corrupt_graph_fail_closed_ok
)
for marker in "${required_markers[@]}"; do
  [[ "$(grep -Fc "marker=$marker" "$LOG")" -eq 1 ]]
done
echo 'marker=settlement_pg18_business_ok cleanup=pending schema=40'
BUSINESS_GATE=1
