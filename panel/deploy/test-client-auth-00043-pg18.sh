#!/usr/bin/env bash
# Real PostgreSQL 18 dual-connection acceptance harness for CLIENT-AUTH-00043.
# It is intentionally not connected to migration/release/production entrypoints.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly PLAN_SHA256='698b42ffe29e2ef219c93dc195a520d61467f74d03f15b8544364d3742823825'
readonly PHASE_SHA256='7108291f38e4f25cf4909d8a5dd12660c36b852d9670c0f8b9322161f9202936'
readonly REDESIGN_SHA256='2d5015f43eefe6eeef257f3d508d06aaabd5258776a047fe69a02fc07de95128'
readonly V3_CONTRACT_SHA256='e8c323762014b5f97d04cf861287026f0d3784d56b2324e1e6b0bc14d833dac7'
readonly LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'

deny() { printf 'client_auth_00043_pg18=DENY reason=%s\n' "$1" >&2; exit "$EXIT_DENIED"; }

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
PLAN="$ROOT/.ai-company/handoffs/client-auth-00043-index-constraints-plan-20260730.md"
PHASE="$ROOT/.ai-company/handoffs/client-auth-00043-00047-phase-decision-20260731.md"
REDESIGN="$ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-20260731.md"
V3_CONTRACT="$ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-v3-20260731.md"
MIGRATION="${AEGIS_CA43_MIGRATION:-$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql}"
RUNNER="${AEGIS_CA43_RUNNER:-$ROOT/deploy/run-client-auth-00043-indexes.sh}"
BASELINE_SQL="${AEGIS_CA43_BASELINE_SQL:-}"
FIXTURE_SQL="${AEGIS_CA43_FIXTURE_SQL:-}"
EVIDENCE_DIR="${AEGIS_CA43_EVIDENCE_DIR:-}"
POSTGRES_IMAGE="${AEGIS_CA43_POSTGRES_IMAGE:-postgres:18.4}"
EXPECTED_IMAGE_ID="${AEGIS_CA43_EXPECTED_POSTGRES_IMAGE_ID:-}"
EXPECTED_MIGRATION_SHA="${AEGIS_CA43_EXPECTED_MIGRATION_SHA256:-}"
EXPECTED_RUNNER_SHA="${AEGIS_CA43_EXPECTED_RUNNER_SHA256:-}"
EXPECTED_BASELINE_SHA="${AEGIS_CA43_EXPECTED_BASELINE_SHA256:-}"
EXPECTED_FIXTURE_SHA="${AEGIS_CA43_EXPECTED_FIXTURE_SHA256:-}"
RELEASE_MANIFEST_FILE="${AEGIS_CA43_RELEASE_MANIFEST_FILE:-}"
EXPECTED_RELEASE_MANIFEST_SHA="${AEGIS_CA43_EXPECTED_RELEASE_MANIFEST_SHA256:-PLACEHOLDER_NO_GO}"

INDEX_NAMES=(
  subscriptions_tenant_id_id_user_id_key
  devices_tenant_id_id_user_id_key
  sessions_tenant_id_id_key
  sessions_tenant_id_id_user_id_key
  sessions_tenant_id_id_device_id_key
  sessions_tenant_id_id_user_id_device_id_key
  device_authorizations_tenant_id_id_key
  device_authorizations_tenant_id_id_device_id_key
  device_authorizations_tenant_user_code_mac_key
  device_tokens_tenant_id_id_key
  device_tokens_tenant_device_id_id_key
  subscription_credentials_tenant_id_id_key
  subscription_credentials_tenant_id_id_sub_user_key
  config_bundles_tenant_id_id_key
  refresh_tokens_tenant_id_id_key
  refresh_tokens_tenant_family_id_id_key
  refresh_tokens_tenant_family_session_device_id_key
  refresh_tokens_tenant_family_generation_key
  refresh_tokens_one_active_family
)

[[ "$(uname -s 2>/dev/null)" == 'Linux' ]] || deny 'native_linux_required'
[[ "$(uname -r 2>/dev/null)" != *[Mm]icrosoft* ]] || deny 'wsl_not_accepted_as_native_runner'
[[ -r /proc/version && "$(cat /proc/version)" != *[Mm]icrosoft* ]] || deny 'native_proc_required'
[[ "$EUID" -eq 0 ]] || deny 'root_harness_required'
for docker_env_name in DOCKER_HOST DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH; do
  [[ -z "${!docker_env_name:-}" ]] || deny "remote_docker_environment:${docker_env_name}"
done

DOCKER_BIN="$(command -v docker 2>/dev/null)" || deny 'docker_not_found'
SHA256_BIN="$(command -v sha256sum 2>/dev/null)" || deny 'sha256sum_not_found'
FLOCK_BIN="$(command -v flock 2>/dev/null)" || deny 'flock_not_found'
SETSID_BIN="$(command -v setsid 2>/dev/null)" || deny 'setsid_not_found'

docker_local() {
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    "$DOCKER_BIN" --host "$LOCAL_DOCKER_HOST" "$@"
}
regular_artifact() {
  local path="$1" label="$2"
  [[ -f "$path" && ! -L "$path" ]] || deny "artifact_missing_or_unsafe:${label}"
}
exact_sha() {
  local path="$1" expected="$2" label="$3"
  [[ "$expected" =~ ^[0-9a-f]{64}$ ]] || deny "expected_sha_missing_or_invalid:${label}"
  [[ "$("$SHA256_BIN" "$path" | awk '{print $1}')" == "$expected" ]] \
    || deny "artifact_sha_mismatch:${label}"
}
manifest_value() {
  local key="$1"
  local -a values=()
  mapfile -t values < <(LC_ALL=C grep -F "${key}=" "$RELEASE_MANIFEST_FILE")
  [[ "${#values[@]}" -eq 1 && "${values[0]}" == "${key}="* ]] \
    || deny "release_manifest_key_invalid:${key}"
  printf '%s' "${values[0]#*=}"
}

regular_artifact "$PLAN" plan
regular_artifact "$PHASE" phase
regular_artifact "$REDESIGN" redesign
regular_artifact "$V3_CONTRACT" v3_contract
exact_sha "$PLAN" "$PLAN_SHA256" plan
exact_sha "$PHASE" "$PHASE_SHA256" phase
exact_sha "$REDESIGN" "$REDESIGN_SHA256" redesign
exact_sha "$V3_CONTRACT" "$V3_CONTRACT_SHA256" v3_contract
regular_artifact "$MIGRATION" migration
regular_artifact "$RUNNER" runner
regular_artifact "$BASELINE_SQL" baseline
regular_artifact "$FIXTURE_SQL" fixture
exact_sha "$MIGRATION" "$EXPECTED_MIGRATION_SHA" migration
exact_sha "$RUNNER" "$EXPECTED_RUNNER_SHA" runner
exact_sha "$BASELINE_SQL" "$EXPECTED_BASELINE_SHA" baseline
exact_sha "$FIXTURE_SQL" "$EXPECTED_FIXTURE_SHA" fixture
[[ -x "$RUNNER" ]] || deny 'runner_not_executable'
[[ "$EXPECTED_RELEASE_MANIFEST_SHA" != 'PLACEHOLDER_NO_GO' ]] \
  || deny 'trusted_release_manifest_placeholder_no_go'
regular_artifact "$RELEASE_MANIFEST_FILE" release_manifest
[[ "$(stat -c '%u:%a:%h' "$RELEASE_MANIFEST_FILE")" == '0:600:1' ]] \
  || deny 'release_manifest_metadata_invalid'
exact_sha "$RELEASE_MANIFEST_FILE" "$EXPECTED_RELEASE_MANIFEST_SHA" release_manifest
PSQL_BIN="$(manifest_value psql_path)"
GOOSE_BIN="$(manifest_value goose_path)"
MIGRATIONS_DIR="$(manifest_value migrations_dir)"
RELEASE_ID="$(manifest_value release_id)"
POST_GOOSE_VERIFIER="$(manifest_value post_goose_verifier_path)"
POST_GOOSE_VERIFIER_SHA="$(manifest_value post_goose_verifier_sha256)"
[[ "$RELEASE_ID" == 'client-auth-00043-v3' ]] || deny 'release_manifest_release_id_invalid'
[[ -x "$PSQL_BIN" && ! -L "$PSQL_BIN" ]] || deny 'manifest_psql_missing_or_unsafe'
[[ -x "$GOOSE_BIN" && ! -L "$GOOSE_BIN" ]] || deny 'manifest_goose_missing_or_unsafe'
[[ -d "$MIGRATIONS_DIR" && ! -L "$MIGRATIONS_DIR" ]] || deny 'manifest_migrations_dir_missing_or_unsafe'
regular_artifact "$POST_GOOSE_VERIFIER" post_goose_verifier
[[ "$POST_GOOSE_VERIFIER" == "$(manifest_value install_root)/deploy/verify-client-auth-00043-post-goose.sql" ]] \
  || deny 'manifest_post_goose_verifier_path_invalid'
exact_sha "$POST_GOOSE_VERIFIER" "$POST_GOOSE_VERIFIER_SHA" post_goose_verifier
[[ "$("$PSQL_BIN" --version)" =~ \(PostgreSQL\)\ 18\. ]] || deny 'manifest_psql_major_18_required'
[[ "$EXPECTED_IMAGE_ID" =~ ^sha256:[0-9a-f]{64}$ ]] || deny 'expected_image_id_required'
[[ "$EVIDENCE_DIR" == /* && ! -e "$EVIDENCE_DIR" ]] || deny 'evidence_dir_must_be_new_absolute_path'
for runner_command in status plan-up execute-up resume-up cleanup-invalid \
  finalize-up plan-down execute-down resume-down finalize-down; do
  grep -Eq "(^|[^A-Za-z0-9_-])${runner_command}([^A-Za-z0-9_-]|$)" "$RUNNER" \
    || deny "runner_cli_contract_missing:${runner_command}"
done
for runner_env in DATABASE_URL PGPASS_FILE RELEASE_MANIFEST_FILE \
  EXPECTED_RELEASE_MANIFEST_SHA256 RUN_ID EXECUTE_APPROVED \
  CIC_STATEMENT_TIMEOUT EVIDENCE_OUT DOWN_EVIDENCE_OUT PRIOR_UP_EVIDENCE_SHA256 \
  EXPECTED_SOURCE_SYSTEM_IDENTIFIER EXPECTED_DATABASE_NAME EXPECTED_DATABASE_OID; do
  if ! grep -Fq "PANDORA_CLIENT_AUTH_00043_${runner_env}" "$RUNNER" &&
    ! grep -Fq "envv ${runner_env}" "$RUNNER"; then
    deny "runner_env_contract_missing:${runner_env}"
  fi
done
grep -Fq 'format=client-auth-00043-evidence-v2' "$RUNNER" \
  || deny 'runner_evidence_v2_contract_missing'
grep -Fq 'client-auth-00043-cleanup-journal-v1' "$RUNNER" \
  || deny 'runner_cleanup_journal_contract_missing'
grep -Fq 'goose_exact_postcheck_failed' "$RUNNER" \
  || deny 'runner_post_goose_contract_missing'
grep -Fq 'test_mode_forbidden' "$RUNNER" \
  || deny 'runner_test_mode_refusal_missing'

[[ "$(docker_local info --format '{{.OSType}}' 2>/dev/null)" == 'linux' ]] \
  || deny 'linux_docker_engine_required'

ACTUAL_IMAGE_ID="$(docker_local image inspect --format '{{.Id}}' "$POSTGRES_IMAGE" 2>/dev/null)" \
  || deny 'postgres18_image_unavailable'
[[ "$ACTUAL_IMAGE_ID" == "$EXPECTED_IMAGE_ID" ]] || deny 'postgres_image_id_mismatch'

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca43-pg18.XXXXXX")" \
  || deny 'temporary_directory_failed'
chmod 0700 "$WORK_DIR"
RUN_TOKEN="$(basename "$WORK_DIR" | tr -cd 'A-Za-z0-9')-$$"
NETWORK="pandora-ca43-accept-$RUN_TOKEN"
CONTAINER="pandora-ca43-accept-$RUN_TOKEN"
PASSWORD_FILE="$WORK_DIR/postgres.password"
PGPASS_FILE="$WORK_DIR/pgpass"
CASES=()
NETWORK_ID=''
CONTAINER_ID=''
CLEANED=0

cleanup() {
  local rc=$?
  if [[ -n "$CONTAINER_ID" ]]; then docker_local rm -f "$CONTAINER_ID" >/dev/null 2>&1 || true; fi
  if [[ -n "$NETWORK_ID" ]]; then docker_local network rm "$NETWORK_ID" >/dev/null 2>&1 || true; fi
  rm -rf -- "$WORK_DIR"
  exit "$rc"
}
trap cleanup EXIT HUP INT TERM

od -An -N32 -tx1 /dev/urandom | tr -d ' \n' >"$PASSWORD_FILE" \
  || deny 'password_generation_failed'
[[ "$(wc -c <"$PASSWORD_FILE")" -eq 64 ]] || deny 'password_generation_invalid'
chmod 0600 "$PASSWORD_FILE"

NETWORK_ID="$(docker_local network create --internal \
  --label pandora.acceptance=client-auth-00043-pg18-v1 "$NETWORK")" \
  || deny 'network_create_failed'
[[ "$NETWORK_ID" =~ ^[0-9a-f]{64}$ ]] || deny 'network_id_invalid'
CONTAINER_ID="$(docker_local run -d --rm --name "$CONTAINER" --network "$NETWORK" \
  -p 127.0.0.1::5432 --tmpfs /var/lib/postgresql:rw,nosuid,nodev,noexec,mode=0700 \
  --mount "type=bind,src=$PASSWORD_FILE,dst=/run/secrets/postgres_password,readonly" \
  --label pandora.acceptance=client-auth-00043-pg18-v1 \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD_FILE=/run/secrets/postgres_password \
  -e POSTGRES_DB=postgres "$ACTUAL_IMAGE_ID")" || deny 'postgres18_container_start_failed'
[[ "$CONTAINER_ID" =~ ^[0-9a-f]{64}$ ]] || deny 'container_id_invalid'

for _ in $(seq 1 120); do
  docker_local exec "$CONTAINER_ID" pg_isready -U postgres -d postgres >/dev/null 2>&1 && break
  sleep 0.25
done
docker_local exec "$CONTAINER_ID" pg_isready -U postgres -d postgres >/dev/null 2>&1 \
  || deny 'postgres18_not_ready'
[[ "$(docker_local exec "$CONTAINER_ID" psql -X -U postgres -d postgres -qtAc \
  "SELECT current_setting('server_version_num')::integer/10000;")" == '18' ]] \
  || deny 'container_postgres_major_mismatch'

PORT_LINE="$(docker_local port "$CONTAINER_ID" 5432/tcp)" || deny 'published_port_missing'
[[ "$PORT_LINE" =~ ^127\.0\.0\.1:([1-9][0-9]{0,4})$ ]] || deny 'published_port_invalid'
HOST_PORT="${BASH_REMATCH[1]}"
IFS= read -r POSTGRES_PASSWORD <"$PASSWORD_FILE"
printf '127.0.0.1:%s:*:postgres:%s\n' "$HOST_PORT" "$POSTGRES_PASSWORD" >"$PGPASS_FILE"
chmod 0600 "$PGPASS_FILE"
unset POSTGRES_PASSWORD

docker_local exec -i "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres -d postgres -f - \
  <"$BASELINE_SQL" >"$WORK_DIR/baseline.out" 2>"$WORK_DIR/baseline.err" \
  || deny 'baseline_restore_failed'
BASE_CHECK="$(docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 -U postgres \
  -d aegis_ca43_base -qtAc "
    SELECT current_setting('server_version_num')::integer/10000 || '|' ||
      coalesce((SELECT max(version_id) FILTER (WHERE is_applied)
        FROM public.goose_db_version),-1) || '|' ||
      (SELECT count(*) FROM app.client_auth_00042_meta);")" \
  || deny 'baseline_contract_query_failed'
[[ "$BASE_CHECK" == '18|42|1' ]] || deny "baseline_contract_mismatch:${BASE_CHECK}"
SOURCE_SYSTEM_IDENTIFIER="$(docker_local exec "$CONTAINER_ID" psql -X -v ON_ERROR_STOP=1 \
  -U postgres -d postgres -qtAc "SELECT system_identifier FROM pg_catalog.pg_control_system();")" \
  || deny 'source_system_identifier_query_failed'
[[ "$SOURCE_SYSTEM_IDENTIFIER" =~ ^[1-9][0-9]*$ ]] || deny 'source_system_identifier_invalid'

mkdir -m 0700 "$EVIDENCE_DIR" || deny 'evidence_dir_create_failed'
printf 'plan_sha256=%s\nphase_sha256=%s\nredesign_sha256=%s\nv3_contract_sha256=%s\nrelease_manifest_sha256=%s\nmigration_sha256=%s\nrunner_sha256=%s\nbaseline_sha256=%s\nfixture_sha256=%s\npostgres_image_id=%s\n' \
  "$PLAN_SHA256" "$PHASE_SHA256" "$REDESIGN_SHA256" "$V3_CONTRACT_SHA256" \
  "$EXPECTED_RELEASE_MANIFEST_SHA" "$EXPECTED_MIGRATION_SHA" "$EXPECTED_RUNNER_SHA" \
  "$EXPECTED_BASELINE_SHA" "$EXPECTED_FIXTURE_SHA" "$ACTUAL_IMAGE_ID" \
  >"$EVIDENCE_DIR/inputs.txt"

db_url() { printf 'postgresql://postgres@127.0.0.1:%s/%s?sslmode=disable\n' "$HOST_PORT" "$1"; }
sql() {
  local db="$1" statement="$2"
  PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt -d "$(db_url "$db")" -c "$statement"
}
new_case() {
  local db="$1"
  [[ "$db" =~ ^ca43_[a-z0-9_]+$ ]] || deny 'case_database_name_invalid'
  docker_local exec "$CONTAINER_ID" createdb -U postgres -T aegis_ca43_base "$db" \
    || deny "case_database_create_failed:${db}"
  CASES+=("$db")
}
fixture() {
  local db="$1" scenario="$2"
  [[ "$scenario" =~ ^[a-z0-9_]+$ ]] || deny 'fixture_scenario_invalid'
  local out
  out="$(PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt \
    -v "scenario=$scenario" -d "$(db_url "$db")" -f "$FIXTURE_SQL")" \
    || deny "fixture_failed:${scenario}"
  [[ "$out" == "CA43-FIXTURE=${scenario}:PASS" ]] || deny "fixture_marker_invalid:${scenario}"
}
runner() {
  local db="$1"; shift
  local db_oid run_id command up_evidence down_evidence prior_up_sha=''
  local cleanup_name='' cleanup_oid='' cleanup_sha='' cleanup_candidate=''
  local cleanup_catalog_file='' cleanup_journal_file='' cleanup_confirm='' i
  db_oid="$(sql "$db" "SELECT oid FROM pg_database WHERE datname=current_database();")" \
    || deny "runner_database_oid_query_failed:${db}"
  [[ "$db_oid" =~ ^[1-9][0-9]*$ ]] || deny "runner_database_oid_invalid:${db}"
  run_id="ca43-${db#ca43_}-${RUN_TOKEN}"
  command="$1"
  up_evidence="$EVIDENCE_DIR/${db}-up.evidence"
  down_evidence="$EVIDENCE_DIR/${db}-down.evidence"
  if [[ -f "$up_evidence" && ! -L "$up_evidence" ]]; then
    prior_up_sha="$("$SHA256_BIN" "$up_evidence" | awk '{print $1}')"
  fi
  if [[ "$command" == cleanup-invalid ]]; then
      cleanup_name="$3"
      cleanup_oid="$5"
      cleanup_sha="$7"
      cleanup_catalog_file="${CLEANUP_CATALOG_FILE:-}"
      [[ -n "$cleanup_catalog_file" ]] || deny 'cleanup_catalog_file_not_bound'
      for i in "${!INDEX_NAMES[@]}"; do
        if [[ "${INDEX_NAMES[$i]}" == "$cleanup_name" ]]; then
          printf -v cleanup_candidate 'U43-%02d' "$((i + 1))"
        fi
      done
      [[ -n "$cleanup_candidate" ]] || deny 'cleanup_candidate_not_allowlisted'
      cleanup_journal_file="$WORK_DIR/${db}-${cleanup_candidate}-${cleanup_oid}.journal"
      {
        printf 'format=client-auth-00043-cleanup-journal-v1\n'
        printf 'run_id=%s\n' "$run_id"
        printf 'source_system_identifier=%s\n' "$SOURCE_SYSTEM_IDENTIFIER"
        printf 'database_name=%s\n' "$db"
        printf 'database_oid=%s\n' "$db_oid"
        printf 'candidate=%s\n' "$cleanup_candidate"
        printf 'name=%s\n' "$cleanup_name"
        printf 'index_oid=%s\n' "$cleanup_oid"
        printf 'catalog_sha256=%s\n' "$cleanup_sha"
        printf 'status=complete\n'
      } >"$cleanup_journal_file"
      chmod 0600 "$cleanup_journal_file"
      sync -f "$cleanup_journal_file" || deny 'cleanup_journal_fsync_failed'
      cleanup_confirm="DROP_EXACT_INVALID:${cleanup_name}:${cleanup_oid}:${cleanup_sha}:${EXPECTED_MIGRATION_SHA}:${EXPECTED_RUNNER_SHA}:${run_id}"
  fi
  env -i PATH="$PATH" HOME="$WORK_DIR" \
    PANDORA_CLIENT_AUTH_00043_DATABASE_URL="$(db_url "$db")" \
    PANDORA_CLIENT_AUTH_00043_PGPASS_FILE="$PGPASS_FILE" \
    PANDORA_CLIENT_AUTH_00043_RELEASE_MANIFEST_FILE="$RELEASE_MANIFEST_FILE" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_RELEASE_MANIFEST_SHA256="$EXPECTED_RELEASE_MANIFEST_SHA" \
    PANDORA_CLIENT_AUTH_00043_RUN_ID="$run_id" \
    PANDORA_CLIENT_AUTH_00043_EXECUTE_APPROVED='approved-isolated-pg18-clone-v1' \
    PANDORA_CLIENT_AUTH_00043_CIC_STATEMENT_TIMEOUT='120s' \
    PANDORA_CLIENT_AUTH_00043_EVIDENCE_OUT="$up_evidence" \
    PANDORA_CLIENT_AUTH_00043_DOWN_EVIDENCE_OUT="$down_evidence" \
    PANDORA_CLIENT_AUTH_00043_PRIOR_UP_EVIDENCE_SHA256="$prior_up_sha" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_SOURCE_SYSTEM_IDENTIFIER="$SOURCE_SYSTEM_IDENTIFIER" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_DATABASE_NAME="$db" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_DATABASE_OID="$db_oid" \
    PANDORA_CLIENT_AUTH_00043_CATALOG_EVIDENCE_FILE="$cleanup_catalog_file" \
    PANDORA_CLIENT_AUTH_00043_CLEANUP_JOURNAL_FILE="$cleanup_journal_file" \
    PANDORA_CLIENT_AUTH_00043_INVALID_CLEANUP_CONFIRM="$cleanup_confirm" \
    "$RUNNER" "$@"
}
goose_direct() {
  local db="$1"; shift
  env -i PATH="$PATH" HOME="$WORK_DIR" PGPASSFILE="$PGPASS_FILE" \
    PGOPTIONS="${DIRECT_GOOSE_PGOPTIONS:-}" \
    GOOSE_DRIVER=postgres GOOSE_DBSTRING="$(db_url "$db")" \
    "$GOOSE_BIN" -dir "$MIGRATIONS_DIR" "$@"
}
expect_runner_deny() {
  local db="$1" label="$2"; shift 2
  set +e
  runner "$db" "$@" >"$WORK_DIR/$label.out" 2>"$WORK_DIR/$label.err"
  local rc=$?
  set -e
  [[ "$rc" -eq 78 ]] || deny "runner_did_not_fail_closed:${label}:${rc}"
}
assert_sql() {
  local db="$1" expected="$2" label="$3" statement="$4" actual
  actual="$(sql "$db" "$statement")" || deny "assert_query_failed:${label}"
  [[ "$actual" == "$expected" ]] || deny "assertion_failed:${label}:${actual}"
}
index_name_list_sql() {
  local name first=1
  for name in "${INDEX_NAMES[@]}"; do
    if [[ "$first" -eq 1 ]]; then
      printf "'%s'" "$name"
      first=0
    else
      printf ",'%s'" "$name"
    fi
  done
}
ALLOWLIST_SQL="$(index_name_list_sql)"

# Direct Goose Up is a control-plane refusal: absent Runner objects must leave
# the only applied waterline at 42 and must not create 00043 meta.
new_case ca43_direct
fixture ca43_direct clean
DIRECT_DB_OID="$(sql ca43_direct "SELECT oid FROM pg_database WHERE datname=current_database();")"
DIRECT_GOOSE_PGOPTIONS="-c aegis.client_auth_00043_finalize_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=direct-forged-up -c aegis.client_auth_00043_evidence_sha256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -c aegis.client_auth_00043_runner_sha256=$EXPECTED_RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$EXPECTED_MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=$SOURCE_SYSTEM_IDENTIFIER -c aegis.client_auth_00043_source_database_name=ca43_direct -c aegis.client_auth_00043_source_database_oid=$DIRECT_DB_OID"
set +e
goose_direct ca43_direct up-by-one >"$WORK_DIR/direct-up.out" 2>"$WORK_DIR/direct-up.err"
DIRECT_UP_RC=$?
set -e
[[ "$DIRECT_UP_RC" -ne 0 ]] || deny 'direct_goose_up_bypassed_runner'
assert_sql ca43_direct '42|0|0' direct_up_waterline_unchanged "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (to_regclass('app.client_auth_00043_meta') IS NOT NULL)::int||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

# PG18-43-01: a second connection cannot acquire the family session lock until
# the first connection releases it.
new_case ca43_normal
fixture ca43_normal clean
PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt -d "$(db_url ca43_normal)" \
  -c "SELECT pg_advisory_lock(420042,1); SELECT pg_sleep(3); SELECT pg_advisory_unlock(420042,1);" \
  >"$WORK_DIR/family-lock-a.out" 2>"$WORK_DIR/family-lock-a.err" &
LOCK_A_PID=$!
sleep 0.25
set +e
PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt -d "$(db_url ca43_normal)" \
  -c "SET statement_timeout='500ms'; SELECT pg_advisory_lock(420042,1);" \
  >"$WORK_DIR/family-lock-b-blocked.out" 2>"$WORK_DIR/family-lock-b-blocked.err"
LOCK_B_RC=$?
set -e
[[ "$LOCK_B_RC" -ne 0 ]] || deny 'family_lock_second_connection_not_blocked'
grep -Fq 'canceling statement due to statement timeout' "$WORK_DIR/family-lock-b-blocked.err" \
  || deny 'family_lock_timeout_evidence_missing'
wait "$LOCK_A_PID" || deny 'family_lock_owner_failed'
LOCK_AFTER_RELEASE="$(sql ca43_normal \
  "SELECT pg_advisory_lock(420042,1); SELECT pg_advisory_unlock(420042,1)::int;")" \
  || deny 'family_lock_reacquire_query_failed'
[[ "${LOCK_AFTER_RELEASE##*$'\n'}" == '1' ]] \
  || deny 'family_lock_not_acquired_after_release'

# PG18-43-02: an ordinary writer commits while CIC is visible through
# pg_stat_progress_create_index.
assert_sql ca43_normal 1 subscriptions_fixture_nonempty \
  "SELECT CASE WHEN EXISTS(SELECT 1 FROM public.subscriptions) THEN 1 ELSE 0 END;"
PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt -d "$(db_url ca43_normal)" \
  -c "BEGIN; UPDATE public.subscriptions SET id=id WHERE ctid=(SELECT ctid FROM public.subscriptions LIMIT 1); SELECT pg_sleep(12); COMMIT;" \
  >"$WORK_DIR/normal-writer.out" 2>"$WORK_DIR/normal-writer.err" &
WRITER_PID=$!
runner ca43_normal execute-up \
  >"$WORK_DIR/normal-runner.out" 2>"$WORK_DIR/normal-runner.err" &
RUNNER_PID=$!
PROGRESS_SEEN=0
for _ in $(seq 1 100); do
  if [[ "$(sql ca43_normal "SELECT count(*) FROM pg_stat_progress_create_index
      WHERE relid='public.subscriptions'::regclass;")" != '0' ]]; then
    PROGRESS_SEEN=1
    break
  fi
  kill -0 "$RUNNER_PID" 2>/dev/null || break
  sleep 0.1
done
[[ "$PROGRESS_SEEN" -eq 1 ]] || deny 'normal_cic_progress_not_observed'
wait "$WRITER_PID" || deny 'normal_writer_failed'
wait "$RUNNER_PID" || deny 'normal_runner_failed'
assert_sql ca43_normal '1|1|1|1|1|1' attach_dependency_exact "
  SELECT i.indisvalid::int||'|'||i.indisready::int||'|'||i.indislive::int||'|'||
    c.convalidated::int||'|'||(c.conindid=x.oid)::int||'|'||
    EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass
      AND d.objid=x.oid AND d.refclassid='pg_constraint'::regclass
      AND d.refobjid=c.oid AND d.deptype='i')::int
  FROM pg_class x JOIN pg_index i ON i.indexrelid=x.oid
  JOIN pg_constraint c ON c.conindid=x.oid
  WHERE x.relname='subscriptions_tenant_id_id_user_id_key';"
assert_sql ca43_normal '42|0|19' execute_up_does_not_write_goose "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (to_regclass('app.client_auth_00043_meta') IS NOT NULL)::int||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

# This negative case isolates source-identity binding. The evidence value is
# intentionally synthetic too, but this case does not claim evidence-only
# cryptographic rejection against a trusted database owner.
NORMAL_DB_OID="$(sql ca43_normal "SELECT oid FROM pg_database WHERE datname=current_database();")"
DIRECT_GOOSE_PGOPTIONS="-c aegis.client_auth_00043_finalize_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=direct-forged-identity -c aegis.client_auth_00043_evidence_sha256=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb -c aegis.client_auth_00043_runner_sha256=$EXPECTED_RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$EXPECTED_MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=1 -c aegis.client_auth_00043_source_database_name=ca43_normal -c aegis.client_auth_00043_source_database_oid=$NORMAL_DB_OID"
set +e
goose_direct ca43_normal up-by-one >"$WORK_DIR/direct-up-forged.out" \
  2>"$WORK_DIR/direct-up-forged.err"
DIRECT_UP_FORGED_RC=$?
set -e
[[ "$DIRECT_UP_FORGED_RC" -ne 0 ]] || deny 'direct_goose_wrong_source_identity_accepted'
assert_sql ca43_normal '42|0|19' direct_up_wrong_source_identity_preserved "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (to_regclass('app.client_auth_00043_meta') IS NOT NULL)::int||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

runner ca43_normal finalize-up >"$WORK_DIR/normal-finalize-up.out" \
  2>"$WORK_DIR/normal-finalize-up.err" || deny 'normal_finalize_up_failed'
assert_sql ca43_normal '43|1|19' finalize_up_exact_waterline "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM app.client_auth_00043_meta)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"
NORMAL_UP_EVIDENCE_SHA="$("$SHA256_BIN" "$EVIDENCE_DIR/ca43_normal-up.evidence" | awk '{print $1}')"

# Duplicate failure must leave an explicitly refused invalid state and must not
# advance to the following allowlisted object.
new_case ca43_duplicate
fixture ca43_duplicate duplicate_u43_09
expect_runner_deny ca43_duplicate duplicate execute-up
assert_sql ca43_duplicate 1 duplicate_left_invalid "
  SELECT count(*) FROM pg_class x JOIN pg_index i ON i.indexrelid=x.oid
  WHERE x.relname='device_authorizations_tenant_user_code_mac_key'
    AND (NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive);"
assert_sql ca43_duplicate 0 duplicate_did_not_advance "
  SELECT count(*) FROM pg_class WHERE relname='device_tokens_tenant_id_id_key';"
sql ca43_duplicate "
  SELECT x.oid||'|'||pg_get_indexdef(x.oid)||'|'||i.indisvalid||'|'||
    i.indisready||'|'||i.indislive||'|'||
    (SELECT count(*) FROM pg_depend d WHERE d.objid=x.oid)
  FROM pg_class x JOIN pg_index i ON i.indexrelid=x.oid
  WHERE x.relname='device_authorizations_tenant_user_code_mac_key';" \
  >"$WORK_DIR/duplicate-catalog.txt"
DUPLICATE_OID="$(cut -d'|' -f1 "$WORK_DIR/duplicate-catalog.txt")"
DUPLICATE_CATALOG_SHA="$("$SHA256_BIN" "$WORK_DIR/duplicate-catalog.txt" | awk '{print $1}')"
[[ "$DUPLICATE_OID" =~ ^[1-9][0-9]*$ && "$DUPLICATE_CATALOG_SHA" =~ ^[0-9a-f]{64}$ ]] \
  || deny 'duplicate_cleanup_evidence_invalid'
CLEANUP_CATALOG_FILE="$WORK_DIR/duplicate-catalog.txt"
expect_runner_deny ca43_duplicate cleanup_wrong_oid cleanup-invalid \
  --name device_authorizations_tenant_user_code_mac_key \
  --expected-oid "$((DUPLICATE_OID + 1))" --catalog-sha256 "$DUPLICATE_CATALOG_SHA"
expect_runner_deny ca43_duplicate cleanup_wrong_sha cleanup-invalid \
  --name device_authorizations_tenant_user_code_mac_key \
  --expected-oid "$DUPLICATE_OID" \
  --catalog-sha256 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
runner ca43_duplicate cleanup-invalid \
  --name device_authorizations_tenant_user_code_mac_key \
  --expected-oid "$DUPLICATE_OID" --catalog-sha256 "$DUPLICATE_CATALOG_SHA" \
  >"$WORK_DIR/cleanup-exact.out" 2>"$WORK_DIR/cleanup-exact.err" \
  || deny 'cleanup_exact_oid_evidence_failed'
assert_sql ca43_duplicate 0 cleanup_exact_removed_only_target "
  SELECT count(*) FROM pg_class
  WHERE relname='device_authorizations_tenant_user_code_mac_key';"

# Same-name replacement of any relation kind is DRIFT and cannot be skipped.
new_case ca43_drift
fixture ca43_drift same_name_drift_u43_03
expect_runner_deny ca43_drift same_name_drift execute-up
assert_sql ca43_drift 1 drift_object_preserved "
  SELECT count(*) FROM pg_class WHERE relname='sessions_tenant_id_id_key'
    AND relkind<>'i';"

# The stale-\gset regression must classify a missing parent/zero-row target as
# DRIFT rather than reusing the previous candidate state.
new_case ca43_zero_row
fixture ca43_zero_row missing_parent_u43_03
expect_runner_deny ca43_zero_row zero_row_drift execute-up
assert_sql ca43_zero_row 0 zero_row_parent_still_missing "
  SELECT count(*) FROM pg_class x JOIN pg_namespace n ON n.oid=x.relnamespace
  WHERE n.nspname='public' AND x.relname='sessions';"

# Kill the actual CIC backend while a normal writer holds the target. Recovery
# must classify ABSENT/INVALID, never silently skip, and may clean only using
# exact OID plus a caller-bound catalog evidence hash.
new_case ca43_interrupt
fixture ca43_interrupt clean
PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt -d "$(db_url ca43_interrupt)" \
  -c "BEGIN; UPDATE public.subscriptions SET id=id WHERE ctid=(SELECT ctid FROM public.subscriptions LIMIT 1); SELECT pg_sleep(30); ROLLBACK;" \
  >"$WORK_DIR/interrupt-writer.out" 2>"$WORK_DIR/interrupt-writer.err" &
INT_WRITER_PID=$!
runner ca43_interrupt execute-up \
  >"$WORK_DIR/interrupt-runner.out" 2>"$WORK_DIR/interrupt-runner.err" &
INT_RUNNER_PID=$!
BACKEND_PID=''
for _ in $(seq 1 100); do
  BACKEND_PID="$(sql ca43_interrupt "SELECT pid FROM pg_stat_progress_create_index
    WHERE relid='public.subscriptions'::regclass ORDER BY pid LIMIT 1;")"
  [[ "$BACKEND_PID" =~ ^[1-9][0-9]*$ ]] && break
  kill -0 "$INT_RUNNER_PID" 2>/dev/null || break
  sleep 0.1
done
[[ "$BACKEND_PID" =~ ^[1-9][0-9]*$ ]] || deny 'interrupt_backend_not_observed'
[[ "$(sql ca43_interrupt "SELECT pg_terminate_backend($BACKEND_PID)::int;")" == '1' ]] \
  || deny 'interrupt_backend_termination_failed'
set +e
wait "$INT_RUNNER_PID"; INT_RUNNER_RC=$?
kill "$INT_WRITER_PID" >/dev/null 2>&1
wait "$INT_WRITER_PID" >/dev/null 2>&1
set -e
[[ "$INT_RUNNER_RC" -ne 0 ]] || deny 'interrupted_runner_reported_success'
INT_STATE="$(sql ca43_interrupt "
  SELECT CASE WHEN x.oid IS NULL THEN 'ABSENT'
    WHEN i.indisvalid AND i.indisready AND i.indislive THEN 'READY_UNATTACHED'
    ELSE 'INVALID_NOT_READY' END
  FROM (SELECT 1) q LEFT JOIN pg_class x
    ON x.relname='subscriptions_tenant_id_id_user_id_key'
  LEFT JOIN pg_index i ON i.indexrelid=x.oid;")"
[[ "$INT_STATE" == 'ABSENT' || "$INT_STATE" == 'INVALID_NOT_READY' ]] \
  || deny "interruption_state_unexpected:${INT_STATE}"
if [[ "$INT_STATE" == 'INVALID_NOT_READY' ]]; then
  sql ca43_interrupt "
    SELECT x.oid||'|'||pg_get_indexdef(x.oid)||'|'||i.indisvalid||'|'||
      i.indisready||'|'||i.indislive||'|'||
      (SELECT count(*) FROM pg_depend d WHERE d.objid=x.oid)
    FROM pg_class x JOIN pg_index i ON i.indexrelid=x.oid
    WHERE x.relname='subscriptions_tenant_id_id_user_id_key';" \
    >"$WORK_DIR/interrupt-catalog.txt"
  INT_OID="${INT_STATE}"
  INT_OID="$(cut -d'|' -f1 "$WORK_DIR/interrupt-catalog.txt")"
  INT_CATALOG_SHA="$("$SHA256_BIN" "$WORK_DIR/interrupt-catalog.txt" | awk '{print $1}')"
  CLEANUP_CATALOG_FILE="$WORK_DIR/interrupt-catalog.txt"
  expect_runner_deny ca43_interrupt interrupt_cleanup_wrong_oid cleanup-invalid \
    --name subscriptions_tenant_id_id_user_id_key --expected-oid "$((INT_OID + 1))" \
    --catalog-sha256 "$INT_CATALOG_SHA"
  runner ca43_interrupt cleanup-invalid \
    --name subscriptions_tenant_id_id_user_id_key --expected-oid "$INT_OID" \
    --catalog-sha256 "$INT_CATALOG_SHA" \
    >"$WORK_DIR/interrupt-recover.out" 2>"$WORK_DIR/interrupt-recover.err" \
    || deny 'attested_invalid_recovery_failed'
fi
runner ca43_interrupt resume-up \
  >"$WORK_DIR/interrupt-resume.out" 2>"$WORK_DIR/interrupt-resume.err" \
  || deny 'interrupted_resume_failed'
assert_sql ca43_interrupt '42|19' resume_up_keeps_unfinalized_waterline "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

# Direct Goose Down must refuse while Runner objects remain. Then a deliberately
# interrupted execute-down must leave waterline 43 until resume-down and
# finalize-down prove complete absence.
DIRECT_GOOSE_PGOPTIONS="-c aegis.client_auth_00043_downgrade_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_callers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=direct-forged-down -c aegis.client_auth_00043_down_evidence_sha256=cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc -c aegis.client_auth_00043_prior_up_evidence_sha256=$NORMAL_UP_EVIDENCE_SHA -c aegis.client_auth_00043_runner_sha256=$EXPECTED_RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$EXPECTED_MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=$SOURCE_SYSTEM_IDENTIFIER -c aegis.client_auth_00043_source_database_name=ca43_normal -c aegis.client_auth_00043_source_database_oid=$NORMAL_DB_OID"
set +e
goose_direct ca43_normal down >"$WORK_DIR/direct-down-objects.out" \
  2>"$WORK_DIR/direct-down-objects.err"
DIRECT_DOWN_OBJECTS_RC=$?
set -e
[[ "$DIRECT_DOWN_OBJECTS_RC" -ne 0 ]] || deny 'direct_goose_down_removed_runner_objects'
assert_sql ca43_normal '43|19' direct_down_objects_preserved "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X -v ON_ERROR_STOP=1 -qAt \
  -d "$(db_url ca43_normal)" \
  -c "BEGIN; LOCK TABLE public.refresh_tokens IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(30); ROLLBACK;" \
  >"$WORK_DIR/down-blocker.out" 2>"$WORK_DIR/down-blocker.err" &
DOWN_BLOCKER_PID=$!
runner ca43_normal execute-down >"$WORK_DIR/partial-down.out" \
  2>"$WORK_DIR/partial-down.err" &
DOWN_RUNNER_PID=$!
DOWN_BACKEND_PID=''
for _ in $(seq 1 100); do
  DOWN_BACKEND_PID="$(sql ca43_normal "SELECT pid FROM pg_stat_activity
    WHERE datname=current_database() AND application_name LIKE 'pandora-client-auth-00043%'
      AND query ~* 'DROP (INDEX|CONSTRAINT)|ALTER TABLE'
    ORDER BY pid LIMIT 1;")"
  [[ "$DOWN_BACKEND_PID" =~ ^[1-9][0-9]*$ ]] && break
  kill -0 "$DOWN_RUNNER_PID" 2>/dev/null || break
  sleep 0.1
done
[[ "$DOWN_BACKEND_PID" =~ ^[1-9][0-9]*$ ]] || deny 'partial_down_backend_not_observed'
[[ "$(sql ca43_normal "SELECT pg_terminate_backend($DOWN_BACKEND_PID)::int;")" == '1' ]] \
  || deny 'partial_down_backend_termination_failed'
set +e
wait "$DOWN_RUNNER_PID"; DOWN_RUNNER_RC=$?
kill "$DOWN_BLOCKER_PID" >/dev/null 2>&1
wait "$DOWN_BLOCKER_PID" >/dev/null 2>&1
set -e
[[ "$DOWN_RUNNER_RC" -ne 0 ]] || deny 'partial_down_runner_reported_success'
assert_sql ca43_normal 43 partial_down_waterline_stays_43 "
  SELECT max(version_id) FILTER (WHERE is_applied) FROM public.goose_db_version;"
runner ca43_normal resume-down >"$WORK_DIR/resume-down.out" \
  2>"$WORK_DIR/resume-down.err" || deny 'resume_down_failed'
assert_sql ca43_normal '43|0|1' resume_down_requires_finalizer "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))||'|'||
    (SELECT count(*) FROM app.client_auth_00043_meta)
  FROM public.goose_db_version;"
runner ca43_normal finalize-down >"$WORK_DIR/finalize-down.out" \
  2>"$WORK_DIR/finalize-down.err" || deny 'finalize_down_failed'
assert_sql ca43_normal '42|0|0' finalize_down_exact_waterline "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (to_regclass('app.client_auth_00043_meta') IS NOT NULL)::int||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

# Down must refuse after a forward-only watermark, and Up must refuse any 00044+
# waterline/evidence before creating the first 00043 name.
new_case ca43_watermark
fixture ca43_watermark clean
runner ca43_watermark execute-up \
  >"$WORK_DIR/watermark-up.out" 2>"$WORK_DIR/watermark-up.err" || deny 'full_up_failed'
runner ca43_watermark finalize-up >"$WORK_DIR/watermark-finalize-up.out" \
  2>"$WORK_DIR/watermark-finalize-up.err" || deny 'watermark_finalize_up_failed'
assert_sql ca43_watermark 19 full_allowlist_complete "
  SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL);"
WATERMARK_DB_OID="$(sql ca43_watermark "SELECT oid FROM pg_database WHERE datname=current_database();")"
WATERMARK_UP_EVIDENCE_SHA="$("$SHA256_BIN" "$EVIDENCE_DIR/ca43_watermark-up.evidence" | awk '{print $1}')"
fixture ca43_watermark set_forward_only_watermark
expect_runner_deny ca43_watermark rollback_refusal execute-down
DIRECT_GOOSE_PGOPTIONS="-c aegis.client_auth_00043_downgrade_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_callers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=direct-watermark-down -c aegis.client_auth_00043_down_evidence_sha256=dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd -c aegis.client_auth_00043_prior_up_evidence_sha256=$WATERMARK_UP_EVIDENCE_SHA -c aegis.client_auth_00043_runner_sha256=$EXPECTED_RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$EXPECTED_MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=$SOURCE_SYSTEM_IDENTIFIER -c aegis.client_auth_00043_source_database_name=ca43_watermark -c aegis.client_auth_00043_source_database_oid=$WATERMARK_DB_OID"
set +e
goose_direct ca43_watermark down >"$WORK_DIR/watermark-direct-down.out" \
  2>"$WORK_DIR/watermark-direct-down.err"
WATERMARK_GOOSE_DOWN_RC=$?
set -e
[[ "$WATERMARK_GOOSE_DOWN_RC" -ne 0 ]] || deny 'watermark_direct_goose_down_not_refused'
assert_sql ca43_watermark 19 rollback_refusal_preserved_objects "
  SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL);"

# A missing parent on Down is DRIFT, never REMOVED_EXACT. The fixture removes
# refresh_tokens and its five U43 objects after a completed/finalized Up.
new_case ca43_down_parent
fixture ca43_down_parent clean
runner ca43_down_parent execute-up >"$WORK_DIR/down-parent-up.out" \
  2>"$WORK_DIR/down-parent-up.err" || deny 'down_parent_prepare_up_failed'
runner ca43_down_parent finalize-up >"$WORK_DIR/down-parent-finalize.out" \
  2>"$WORK_DIR/down-parent-finalize.err" || deny 'down_parent_prepare_finalize_failed'
fixture ca43_down_parent missing_parent_down_u43_19
expect_runner_deny ca43_down_parent missing_parent_down_drift execute-down
assert_sql ca43_down_parent '43|1|0' missing_parent_down_preserved_control_plane "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM app.client_auth_00043_meta)||'|'||
    (to_regclass('public.refresh_tokens') IS NOT NULL)::int
  FROM public.goose_db_version;"

# Known pre-00044 future boundaries must refuse before the first Down mutation.
new_case ca43_generated
fixture ca43_generated clean
runner ca43_generated execute-up >"$WORK_DIR/generated-up.out" \
  2>"$WORK_DIR/generated-up.err" || deny 'generated_prepare_up_failed'
runner ca43_generated finalize-up >"$WORK_DIR/generated-finalize.out" \
  2>"$WORK_DIR/generated-finalize.err" || deny 'generated_prepare_finalize_failed'
fixture ca43_generated set_generated_column_future
expect_runner_deny ca43_generated generated_future_refusal execute-down
assert_sql ca43_generated '43|1|19' generated_future_preserved_objects "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM app.client_auth_00043_meta)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

new_case ca43_runtime
fixture ca43_runtime clean
runner ca43_runtime execute-up >"$WORK_DIR/runtime-up.out" \
  2>"$WORK_DIR/runtime-up.err" || deny 'runtime_prepare_up_failed'
runner ca43_runtime finalize-up >"$WORK_DIR/runtime-finalize.out" \
  2>"$WORK_DIR/runtime-finalize.err" || deny 'runtime_prepare_finalize_failed'
fixture ca43_runtime set_runtime_future_row
expect_runner_deny ca43_runtime runtime_future_refusal execute-down
assert_sql ca43_runtime '43|1|19' runtime_future_preserved_objects "
  SELECT max(version_id) FILTER (WHERE is_applied)||'|'||
    (SELECT count(*) FROM app.client_auth_00043_meta)||'|'||
    (SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL))
  FROM public.goose_db_version;"

new_case ca43_future
fixture ca43_future set_goose_00044_applied
expect_runner_deny ca43_future future_refusal execute-up
assert_sql ca43_future 0 future_refusal_no_00043_residue "
  SELECT count(*) FROM pg_class WHERE relname IN ($ALLOWLIST_SQL);"

# Remove all case databases before destroying the disposable cluster.
for db in "${CASES[@]}"; do
  docker_local exec "$CONTAINER_ID" dropdb -U postgres --force "$db" \
    || deny "case_database_cleanup_failed:${db}"
done
docker_local exec "$CONTAINER_ID" dropdb -U postgres --force aegis_ca43_base \
  || deny 'baseline_database_cleanup_failed'
docker_local rm -f "$CONTAINER_ID" >/dev/null || deny 'container_cleanup_failed'
docker_local inspect --type container "$CONTAINER_ID" >/dev/null 2>&1 \
  && deny 'container_cleanup_not_confirmed'
CONTAINER_ID=''
docker_local network rm "$NETWORK_ID" >/dev/null || deny 'network_cleanup_failed'
docker_local network inspect "$NETWORK_ID" >/dev/null 2>&1 \
  && deny 'network_cleanup_not_confirmed'
NETWORK_ID=''
CLEANED=1

printf 'client_auth_00043_pg18=PASS contract=v3 cases=normal,duplicate,drift,interrupt,watermark,down_parent,generated,runtime,future clean_residue=true\n' \
  | tee "$EVIDENCE_DIR/result.txt"
