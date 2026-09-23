#!/usr/bin/env bash
# CLIENT-AUTH-00043 resumable data-plane runner. Not wired to production.
set -Eeuo pipefail
umask 077

readonly EXIT_DENIED=78
readonly V3_CONTRACT_SHA='E8C323762014B5F97D04CF861287026F0D3784D56B2324E1E6B0BC14D833DAC7'
readonly FUTURE_GATE_CONTRACT='client-auth-00043-known-pre00044-v1'
readonly REDESIGN_SHA='2D5015F43EEFE6EEEF257F3D508D06AAABD5258776A047FE69A02FC07DE95128'
readonly PLAN_SHA='698B42FFE29E2EF219C93DC195A520D61467F74D03F15B8544364D3742823825'
readonly PHASE_SHA='7108291F38E4F25CF4909D8A5DD12660C36B852D9670C0F8B9322161F9202936'
readonly MIGRATION_42_SHA='FFAF84B6E73EB0EEF6794F5CA72859607B0C4C5BF313D9F7548DAE120848DFF5'
readonly CONTRACT_SHA='4FC9582DE7121A50FCFF6D65D7C96ACBDF9BDBD4E9C40AD8BFC8F43919B3608E'

deny() { printf 'client_auth_00043=DENY reason=%s\n' "$1" >&2; exit "$EXIT_DENIED"; }
sha_file() { sha256sum -- "$1" | awk '{print tolower($1)}'; }

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MIGRATION="$REPO_ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql"
RUNNER_SHA="$(sha_file "$0")"
MIGRATION_SHA="$(sha_file "$MIGRATION")"

verify_sha() {
  [[ -f "$1" && ! -L "$1" ]] || deny "frozen_input_missing:$3"
  [[ "$(sha_file "$1")" == "${2,,}" ]] || deny "frozen_input_sha_mismatch:$3"
}
verify_sha "$REPO_ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-20260731.md" "$REDESIGN_SHA" redesign
verify_sha "$REPO_ROOT/.ai-company/handoffs/client-auth-00043-runner-goose-redesign-v3-20260731.md" "$V3_CONTRACT_SHA" v3_contract
verify_sha "$REPO_ROOT/.ai-company/handoffs/client-auth-00043-index-constraints-plan-20260730.md" "$PLAN_SHA" plan
verify_sha "$REPO_ROOT/.ai-company/handoffs/client-auth-00043-00047-phase-decision-20260731.md" "$PHASE_SHA" phase
verify_sha "$REPO_ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql" "$MIGRATION_42_SHA" migration42

readonly -a IDS=(U43-01 U43-02 U43-03 U43-04 U43-05 U43-06 U43-07 U43-08 U43-09 U43-10 U43-11 U43-12 U43-13 U43-14 U43-15 U43-16 U43-17 U43-18 U43-19)
readonly -a TABLES=(subscriptions devices sessions sessions sessions sessions device_authorizations device_authorizations device_authorizations device_tokens device_tokens subscription_credentials subscription_credentials config_bundles refresh_tokens refresh_tokens refresh_tokens refresh_tokens refresh_tokens)
readonly -a NAMES=(
  subscriptions_tenant_id_id_user_id_key devices_tenant_id_id_user_id_key
  sessions_tenant_id_id_key sessions_tenant_id_id_user_id_key sessions_tenant_id_id_device_id_key
  sessions_tenant_id_id_user_id_device_id_key device_authorizations_tenant_id_id_key
  device_authorizations_tenant_id_id_device_id_key device_authorizations_tenant_user_code_mac_key
  device_tokens_tenant_id_id_key device_tokens_tenant_device_id_id_key
  subscription_credentials_tenant_id_id_key subscription_credentials_tenant_id_id_sub_user_key
  config_bundles_tenant_id_id_key refresh_tokens_tenant_id_id_key
  refresh_tokens_tenant_family_id_id_key refresh_tokens_tenant_family_session_device_id_key
  refresh_tokens_tenant_family_generation_key refresh_tokens_one_active_family
)
readonly -a COLUMNS=(
  'tenant_id, id, user_id' 'tenant_id, id, user_id' 'tenant_id, id'
  'tenant_id, id, user_id' 'tenant_id, id, device_id' 'tenant_id, id, user_id, device_id'
  'tenant_id, id' 'tenant_id, id, device_id' 'tenant_id, user_code_mac'
  'tenant_id, id' 'tenant_id, device_id, id' 'tenant_id, id'
  'tenant_id, id, subscription_id, user_id' 'tenant_id, id' 'tenant_id, id'
  'tenant_id, family_id, id' 'tenant_id, family_id, session_id, device_id, id'
  'tenant_id, family_id, generation' 'tenant_id, family_id'
)

usage() {
  printf '%s\n' 'usage: status | plan-up | execute-up | resume-up | cleanup-invalid --name NAME --expected-oid OID --catalog-sha256 SHA | finalize-up | plan-down | execute-down | resume-down | finalize-down' >&2
  exit "$EXIT_DENIED"
}
[[ $# -ge 1 ]] || usage
COMMAND="$1"; shift
case "$COMMAND" in
  status|plan-up|execute-up|resume-up|finalize-up|plan-down|execute-down|resume-down|finalize-down) [[ $# -eq 0 ]] || usage ;;
  cleanup-invalid)
    CLEANUP_NAME=''; EXPECTED_OID=''; CATALOG_SHA=''
    while (($#)); do
      [[ $# -ge 2 ]] || usage
      case "$1" in
        --name) [[ -z "$CLEANUP_NAME" ]] || usage; CLEANUP_NAME="$2" ;;
        --expected-oid) [[ -z "$EXPECTED_OID" ]] || usage; EXPECTED_OID="$2" ;;
        --catalog-sha256) [[ -z "$CATALOG_SHA" ]] || usage; CATALOG_SHA="$2" ;;
        *) usage ;;
      esac
      shift 2
    done
    [[ -n "$CLEANUP_NAME" && "$EXPECTED_OID" =~ ^[1-9][0-9]{0,9}$ && "$CATALOG_SHA" =~ ^[0-9a-f]{64}$ ]] || usage
    ;;
  *) usage ;;
esac

is_plan=false
[[ "$COMMAND" == plan-up || "$COMMAND" == plan-down ]] && is_plan=true
is_finalize=false
[[ "$COMMAND" == finalize-up || "$COMMAND" == finalize-down ]] && is_finalize=true
is_execute=false
[[ "$COMMAND" == execute-up || "$COMMAND" == resume-up || "$COMMAND" == execute-down || "$COMMAND" == resume-down || "$COMMAND" == cleanup-invalid || "$is_finalize" == true ]] && is_execute=true

envv() { local name="PANDORA_CLIENT_AUTH_00043_$1"; printf '%s' "${!name:-}"; }
RUN_ID="$(envv RUN_ID)"
DB_URL="$(envv DATABASE_URL)"
PGPASS_FILE="$(envv PGPASS_FILE)"
EVIDENCE_OUT="$(envv EVIDENCE_OUT)"
DOWN_EVIDENCE_OUT="$(envv DOWN_EVIDENCE_OUT)"
RELEASE_MANIFEST_FILE="$(envv RELEASE_MANIFEST_FILE)"
EXPECTED_RELEASE_MANIFEST_SHA="$(envv EXPECTED_RELEASE_MANIFEST_SHA256)"
EXPECTED_SYSTEM="$(envv EXPECTED_SOURCE_SYSTEM_IDENTIFIER)"
EXPECTED_DB_NAME="$(envv EXPECTED_DATABASE_NAME)"
EXPECTED_DB_OID="$(envv EXPECTED_DATABASE_OID)"
CIC_TIMEOUT="$(envv CIC_STATEMENT_TIMEOUT)"

declare -A RELEASE=()
validate_root_artifact() {
  local path="$1" mode
  [[ -f "$path" && ! -L "$path" ]] || deny release_artifact_invalid
  [[ "$(stat -c '%u:%h' "$path")" == '0:1' ]] || deny release_artifact_owner_or_link_invalid
  mode="$(stat -c '%a' "$path")"
  (( (8#$mode & 8#022) == 0 )) || deny release_artifact_group_world_writable
}

load_release_manifest() {
  [[ "$EXPECTED_RELEASE_MANIFEST_SHA" =~ ^[0-9a-f]{64}$ ]] || deny expected_release_manifest_sha_invalid
  [[ -f "$RELEASE_MANIFEST_FILE" && ! -L "$RELEASE_MANIFEST_FILE" ]] || deny release_manifest_file_invalid
  [[ "$(stat -c '%u:%a:%h' "$RELEASE_MANIFEST_FILE")" == '0:600:1' ]] || deny release_manifest_metadata_invalid
  [[ "$(sha_file "$RELEASE_MANIFEST_FILE")" == "$EXPECTED_RELEASE_MANIFEST_SHA" ]] || deny release_manifest_sha_mismatch
  mapfile -t manifest_lines <"$RELEASE_MANIFEST_FILE" || deny release_manifest_read_failed
  printf '%s\n' "${manifest_lines[@]}" | LC_ALL=C grep -Fxq 'status=PLACEHOLDER_NO_GO' \
    && deny release_manifest_placeholder_no_go
  ((${#manifest_lines[@]} >= 20)) || deny release_manifest_too_short
  local line key value
  for line in "${manifest_lines[@]}"; do
    [[ -n "$line" && ${#line} -le 4096 && "$line" != *$'\r'* && "$line" == *=* ]] || deny release_manifest_line_invalid
    key="${line%%=*}"; value="${line#*=}"
    if [[ "$key" == migration ]]; then
      [[ "$value" =~ ^[A-Za-z0-9._/-]+\.sql\|[1-9][0-9]*\|[0-9a-f]{64}$ ]] || deny release_manifest_inventory_line_invalid
      continue
    fi
    case "$key" in
      format|status|release_id|architecture|v3_contract_sha256|install_root|migrations_dir|runner_path|runner_sha256|migration_00043_path|migration_00043_sha256|psql_path|psql_sha256|psql_version|goose_path|goose_sha256|goose_version|post_goose_verifier_path|post_goose_verifier_sha256|inventory_count|inventory_sha256) ;;
      *) deny "release_manifest_unknown_key:$key" ;;
    esac
    [[ ! -v "RELEASE[$key]" ]] || deny "release_manifest_duplicate_key:$key"
    RELEASE["$key"]="$value"
  done
  readonly -a manifest_header_keys=(format status release_id architecture v3_contract_sha256 install_root migrations_dir runner_path runner_sha256 migration_00043_path migration_00043_sha256 psql_path psql_sha256 psql_version goose_path goose_sha256 goose_version post_goose_verifier_path post_goose_verifier_sha256 inventory_count)
  for index in "${!manifest_header_keys[@]}"; do
    [[ "${manifest_lines[$index]}" == "${manifest_header_keys[$index]}="* ]] || deny "release_manifest_key_order_invalid:${manifest_header_keys[$index]}"
  done
  [[ "${manifest_lines[-1]}" == inventory_sha256=* ]] || deny release_manifest_trailer_invalid
  [[ "${RELEASE[format]:-}" == client-auth-00043-v3-release-manifest ]] || deny release_manifest_format_invalid
  [[ "${RELEASE[status]:-}" != PLACEHOLDER_NO_GO ]] || deny release_manifest_placeholder_no_go
  [[ "${RELEASE[status]:-}" == TRUSTED ]] || deny release_manifest_status_invalid
  [[ "${RELEASE[v3_contract_sha256]:-}" == "$V3_CONTRACT_SHA" ]] || deny release_manifest_contract_sha_mismatch
  [[ "${RELEASE[runner_sha256]:-}" == "$RUNNER_SHA" && "${RELEASE[migration_00043_sha256]:-}" == "$MIGRATION_SHA" ]] || deny release_manifest_source_sha_mismatch
  [[ "${RELEASE[runner_path]:-}" == "$(realpath -e "$0")" && "${RELEASE[migration_00043_path]:-}" == "$(realpath -e "$MIGRATION")" ]] || deny release_manifest_source_path_mismatch
  [[ "${RELEASE[release_id]:-}" == client-auth-00043-v3 ]] || deny release_manifest_release_id_invalid
  [[ "${RELEASE[architecture]:-}" == amd64 || "${RELEASE[architecture]:-}" == arm64 ]] || deny release_manifest_architecture_invalid
  [[ "${RELEASE[inventory_count]:-}" =~ ^[1-9][0-9]{0,4}$ && "${RELEASE[inventory_sha256]:-}" =~ ^[0-9a-f]{64}$ ]] || deny release_manifest_inventory_header_invalid
  [[ "${#manifest_lines[@]}" -eq $((21 + RELEASE[inventory_count])) ]] || deny release_manifest_total_line_count_invalid
  for key in install_root migrations_dir runner_path migration_00043_path psql_path goose_path post_goose_verifier_path; do
    [[ "${RELEASE[$key]:-}" == /* && "${RELEASE[$key]}" == "$(realpath -e "${RELEASE[$key]}")" ]] || deny "release_manifest_realpath_invalid:$key"
  done
  [[ "${RELEASE[migrations_dir]}" == "${RELEASE[install_root]}"/* ]] || deny release_manifest_migrations_outside_install_root
  [[ "${RELEASE[post_goose_verifier_path]}" == "${RELEASE[install_root]}/deploy/verify-client-auth-00043-post-goose.sql" ]] \
    || deny release_manifest_post_goose_verifier_path_mismatch
  [[ "${RELEASE[psql_sha256]:-}" =~ ^[0-9a-f]{64}$ &&
     "${RELEASE[goose_sha256]:-}" =~ ^[0-9a-f]{64}$ &&
     "${RELEASE[post_goose_verifier_sha256]:-}" =~ ^[0-9a-f]{64}$ ]] \
    || deny release_manifest_artifact_sha_invalid
  case "$(uname -m)" in
    x86_64|amd64) [[ "${RELEASE[architecture]}" == amd64 ]] || deny release_manifest_architecture_mismatch ;;
    aarch64|arm64) [[ "${RELEASE[architecture]}" == arm64 ]] || deny release_manifest_architecture_mismatch ;;
    *) deny release_manifest_host_architecture_unsupported ;;
  esac
  validate_root_artifact "${RELEASE[runner_path]}"; validate_root_artifact "${RELEASE[migration_00043_path]}"
  validate_root_artifact "${RELEASE[psql_path]}"; validate_root_artifact "${RELEASE[goose_path]}"
  validate_root_artifact "${RELEASE[post_goose_verifier_path]}"
  [[ "$(sha_file "${RELEASE[psql_path]}")" == "${RELEASE[psql_sha256]}" && "$(sha_file "${RELEASE[goose_path]}")" == "${RELEASE[goose_sha256]}" ]] || deny release_manifest_binary_content_mismatch
  [[ "$(sha_file "${RELEASE[post_goose_verifier_path]}")" == "${RELEASE[post_goose_verifier_sha256]}" ]] \
    || deny release_manifest_post_goose_verifier_content_mismatch
  [[ "$("${RELEASE[psql_path]}" --version | tr -d '\r\n')" == "${RELEASE[psql_version]:-}" ]] || deny release_manifest_psql_version_mismatch
  [[ "$("${RELEASE[goose_path]}" -version | tr -d '\r\n')" == "${RELEASE[goose_version]:-}" ]] || deny release_manifest_goose_version_mismatch
  local inventory_file expected_file inventory_count="${RELEASE[inventory_count]}"
  inventory_file="$(mktemp "${TMPDIR:-/tmp}/client-auth-00043-inventory.XXXXXX")"
  expected_file="$(mktemp "${TMPDIR:-/tmp}/client-auth-00043-inventory-expected.XXXXXX")"
  while IFS= read -r sql_file; do
    [[ -f "$sql_file" && ! -L "$sql_file" ]] || deny release_manifest_sql_entry_unsafe
    relative="${sql_file#"${RELEASE[migrations_dir]}"/}"
    printf 'migration=%s|%s|%s\n' "$relative" "$(stat -c '%s' "$sql_file")" "$(sha_file "$sql_file")"
  done < <(find "${RELEASE[migrations_dir]}" -maxdepth 1 -name '*.sql' -print | LC_ALL=C sort) >"$inventory_file"
  printf '%s\n' "${manifest_lines[@]}" | grep '^migration=' >"$expected_file" || deny release_manifest_inventory_missing
  [[ "$(wc -l <"$inventory_file" | tr -d ' ')" == "$inventory_count" && "$(wc -l <"$expected_file" | tr -d ' ')" == "$inventory_count" ]] || deny release_manifest_inventory_count_mismatch
  cmp -s "$inventory_file" "$expected_file" || deny release_manifest_inventory_drift
  [[ "$(sha_file "$inventory_file")" == "${RELEASE[inventory_sha256]}" ]] || deny release_manifest_inventory_sha_mismatch
  rm -f "$inventory_file" "$expected_file"
  PSQL_BIN="${RELEASE[psql_path]}"; GOOSE_BIN="${RELEASE[goose_path]}"; MIGRATIONS_DIR="${RELEASE[migrations_dir]}"
  POST_GOOSE_VERIFIER="${RELEASE[post_goose_verifier_path]}"
  RELEASE_ID="${RELEASE[release_id]}"; RELEASE_MANIFEST_SHA="$EXPECTED_RELEASE_MANIFEST_SHA"
}

if [[ "$is_execute" == true ]]; then
  [[ -z "$(envv TEST_MODE)" ]] || deny test_mode_forbidden
  [[ "$EUID" -eq 0 ]] || deny root_required
  [[ "$(envv EXECUTE_APPROVED)" == 'approved-isolated-pg18-clone-v1' ]] || deny execute_approval_missing
  [[ "$RUN_ID" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ ]] || deny run_id_invalid
  [[ -n "$DB_URL" ]] || deny database_url_required
  [[ "$DB_URL" != *$'\n'* && "$DB_URL" != *$'\r'* ]] || deny database_url_newline
  [[ "$DB_URL" =~ ^postgres(ql)?://[^/]+/[A-Za-z_][A-Za-z0-9_-]{0,62}([?].*)?$ ]] || deny database_url_invalid
  [[ ! "$DB_URL" =~ ://[^/@:]+:[^/@]+@ && ! "$DB_URL" =~ [\?\&]password= ]] || deny database_url_must_not_contain_password
  [[ -f "$PGPASS_FILE" && ! -L "$PGPASS_FILE" ]] || deny pgpass_file_invalid
  [[ "$(stat -c '%u:%a:%h' "$PGPASS_FILE")" == '0:600:1' ]] || deny pgpass_file_not_root_only
  [[ "$EXPECTED_SYSTEM" =~ ^[1-9][0-9]{0,19}$ ]] || deny expected_system_identifier_invalid
  [[ "$EXPECTED_DB_NAME" =~ ^[A-Za-z_][A-Za-z0-9_-]{0,62}$ ]] || deny expected_database_name_invalid
  [[ "$EXPECTED_DB_OID" =~ ^[1-9][0-9]{0,9}$ ]] || deny expected_database_oid_invalid
  [[ "$CIC_TIMEOUT" =~ ^[1-9][0-9]{0,5}s$ ]] || deny cic_timeout_not_frozen
  load_release_manifest
fi

expected_def() {
  local i="$1" suffix=''
  [[ "$i" -eq 18 ]] && suffix=" WHERE (status = 'active'::text)"
  printf 'CREATE UNIQUE INDEX %s ON public.%s USING btree (%s)%s' "${NAMES[$i]}" "${TABLES[$i]}" "${COLUMNS[$i]}" "$suffix"
}

non_null_filter() {
  local result='' col
  IFS=',' read -ra parts <<<"$1"
  for col in "${parts[@]}"; do col="${col// /}"; [[ -z "$result" ]] || result+=' AND '; result+="$col IS NOT NULL"; done
  printf '%s' "$result"
}

null_profile_expr() {
  local result='' col
  IFS=',' read -ra parts <<<"$1"
  for col in "${parts[@]}"; do
    col="${col// /}"
    [[ -z "$result" ]] || result+=','
    result+="'$col',count(*) FILTER (WHERE $col IS NULL)"
  done
  printf 'jsonb_build_object(%s)' "$result"
}

emit_header() {
  local direction="$1" goose='42' meta="to_regclass('app.client_auth_00043_meta') IS NULL"
  [[ "$direction" == down ]] && { goose='43'; meta="to_regclass('app.client_auth_00043_meta') IS NOT NULL"; }
  cat <<SQL
\set ON_ERROR_STOP on
\pset tuples_only on
\pset format unaligned
\set VERBOSITY terse
SET application_name='pandora-client-auth-00043:${RUN_ID:-plan}';
SET lock_timeout='5s';
SET statement_timeout='30s';
SELECT pg_backend_pid() AS pinned_backend_pid \gset
SELECT pg_advisory_lock(420042,1);
SELECT CASE WHEN current_setting('server_version_num')::integer BETWEEN 180000 AND 189999 THEN 1 ELSE 1/0 END;
SELECT CASE WHEN (SELECT system_identifier::text FROM pg_control_system())='${EXPECTED_SYSTEM:-1}'
 AND current_database()='${EXPECTED_DB_NAME:-plan_db}'
 AND (SELECT oid::text FROM pg_database WHERE datname=current_database())='${EXPECTED_DB_OID:-1}' THEN 1 ELSE 1/0 END;
SELECT CASE WHEN (SELECT max(version_id) FILTER (WHERE is_applied) FROM public.goose_db_version)=${goose}
 AND NOT EXISTS(SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>${goose})
 AND ${meta} THEN 1 ELSE 1/0 END;
SQL
}

emit_boundary() {
  local direction="$1" goose=42
  [[ "$direction" == down ]] && goose=43
  local meta_boundary="to_regclass('app.client_auth_00043_meta') IS NULL"
  [[ "$direction" == down ]] && meta_boundary="to_regclass('app.client_auth_00043_meta') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM app.client_auth_00043_meta WHERE legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL OR cutover_at IS NOT NULL OR constraints_validated_at IS NOT NULL)"
  cat <<SQL
SELECT CASE WHEN pg_backend_pid()=:'pinned_backend_pid'::integer THEN 1 ELSE 1/0 END;
SELECT CASE WHEN EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid() AND classid=420042 AND objid=1 AND granted) THEN 1 ELSE 1/0 END;
SELECT CASE WHEN (SELECT system_identifier::text FROM pg_control_system())='${EXPECTED_SYSTEM:-1}'
 AND current_database()='${EXPECTED_DB_NAME:-plan_db}'
 AND (SELECT oid::text FROM pg_database WHERE datname=current_database())='${EXPECTED_DB_OID:-1}'
 AND (SELECT max(version_id) FILTER (WHERE is_applied) FROM public.goose_db_version)=${goose}
 AND NOT EXISTS(SELECT 1 FROM public.goose_db_version WHERE is_applied AND version_id>${goose})
 AND NOT EXISTS(SELECT 1 FROM app.client_auth_00042_meta WHERE legacy_revocation_at IS NOT NULL OR first_client_write_at IS NOT NULL OR cutover_at IS NOT NULL OR constraints_validated_at IS NOT NULL)
 AND EXISTS(SELECT 1 FROM app.client_auth_00042_meta WHERE singleton AND catalog_manifest->>'format'='client-auth-00042-catalog-v1' AND jsonb_typeof(catalog_manifest->'relations')='array')
 AND ${meta_boundary}
 AND NOT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='public' AND table_name='subscription_credentials' AND is_generated='ALWAYS')
 AND (SELECT count(*) FROM public.config_bundle_nodes)=0
 AND (SELECT count(*) FROM public.refresh_families)=0
 AND (SELECT count(*) FROM public.device_proof_nonces)=0
 AND (SELECT count(*) FROM public.client_access_token_jtis)=0
 AND (SELECT count(*) FROM public.device_issuance_response_replays)=0
 AND (SELECT count(*) FROM public.client_refresh_response_replays)=0
 AND (SELECT count(*) FROM public.device_issuance_replay_uses)=0
 AND (SELECT count(*) FROM public.client_refresh_replay_uses)=0
 THEN 1 ELSE 1/0 END;
SQL
}

emit_classify() {
  local i="$1"
  local direction="${2:-up}" absent_state='ABSENT'
  [[ "$direction" == down ]] && absent_state='REMOVED_EXACT'
  local name="${NAMES[$i]}" table="${TABLES[$i]}" def partial=false
  def="$(expected_def "$i")"; [[ "$i" -eq 18 ]] && partial=true
  cat <<SQL
\unset candidate_state state_absent state_invalid state_ready state_attached state_partial state_removed state_drift candidate_oid
WITH target AS (
 SELECT tc.oid table_oid,tc.relowner table_owner,tc.relkind parent_kind,
  tc.relpersistence parent_persistence,ic.oid index_oid,ic.relowner index_owner,
  ic.relkind index_kind,ic.relpersistence index_persistence,ic.reltablespace,ic.reloptions,
  ix.indisunique,ix.indimmediate,ix.indisvalid,ix.indisready,ix.indislive,ix.indnullsnotdistinct,
  ix.indexprs,ix.indpred,am.amname,pg_get_indexdef(ic.oid) indexdef,
  con.oid constraint_oid,con.contype,con.convalidated,con.condeferrable,con.condeferred,con.conindid,
  (SELECT count(*) FROM pg_namespace sn WHERE sn.nspname='public') schema_count,
  (SELECT count(*) FROM pg_class pc JOIN pg_namespace pn ON pn.oid=pc.relnamespace WHERE pn.nspname='public' AND pc.relname='${table}') parent_count,
  (SELECT count(*) FROM pg_class x JOIN pg_namespace xn ON xn.oid=x.relnamespace WHERE xn.nspname='public' AND x.relname='${name}') class_count,
  (SELECT count(*) FROM pg_constraint x JOIN pg_namespace xn ON xn.oid=x.connamespace WHERE xn.nspname='public' AND x.conname='${name}') constraint_count
 FROM pg_class tc JOIN pg_namespace n ON n.oid=tc.relnamespace
 LEFT JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname='${name}'
 LEFT JOIN pg_index ix ON ix.indexrelid=ic.oid LEFT JOIN pg_am am ON am.oid=ic.relam
 LEFT JOIN pg_constraint con ON con.connamespace=n.oid AND con.conname='${name}' AND con.conrelid=tc.oid
 WHERE n.nspname='public' AND tc.relname='${table}'
), classified AS (
 SELECT CASE
  WHEN schema_count=1 AND parent_count=1 AND parent_kind='r' AND parent_persistence='p'
   AND table_owner IS NOT NULL AND coalesce(class_count,0)=0 AND coalesce(constraint_count,0)=0 THEN '${absent_state}'
  WHEN schema_count=1 AND parent_count=1 AND parent_kind='r' AND parent_persistence='p'
   AND class_count=1 AND constraint_count=0 AND index_kind='i' AND index_owner=table_owner
   AND index_persistence='p' AND reltablespace=0 AND reloptions IS NULL AND amname='btree'
   AND indisunique AND indimmediate AND NOT indnullsnotdistinct AND indexprs IS NULL
   AND indexdef='${def}' AND (NOT indisvalid OR NOT indisready OR NOT indislive) THEN 'INVALID_EXACT'
  WHEN schema_count=1 AND parent_count=1 AND parent_kind='r' AND parent_persistence='p'
   AND class_count=1 AND constraint_count=0 AND index_kind='i' AND index_owner=table_owner
   AND index_persistence='p' AND reltablespace=0 AND reloptions IS NULL AND amname='btree'
   AND indisunique AND indimmediate AND indisvalid AND indisready AND indislive
   AND NOT indnullsnotdistinct AND indexprs IS NULL AND indexdef='${def}'
   THEN CASE WHEN ${partial} THEN 'PARTIAL_EXACT' ELSE 'READY_UNATTACHED' END
  WHEN schema_count=1 AND parent_count=1 AND parent_kind='r' AND parent_persistence='p'
   AND NOT ${partial} AND class_count=1 AND constraint_count=1 AND index_kind='i'
   AND index_owner=table_owner AND index_persistence='p' AND reltablespace=0
   AND reloptions IS NULL AND amname='btree' AND indisunique AND indimmediate
   AND indisvalid AND indisready AND indislive AND NOT indnullsnotdistinct
   AND indexprs IS NULL AND indpred IS NULL AND indexdef='${def}' AND contype='u'
   AND convalidated AND NOT condeferrable AND NOT condeferred AND conindid=index_oid
   AND (SELECT count(*) FROM pg_depend d
        WHERE d.classid='pg_class'::regclass AND d.objid=index_oid AND d.objsubid=0
          AND d.refclassid='pg_constraint'::regclass AND d.refobjid=constraint_oid
          AND d.refobjsubid=0 AND d.deptype='i')=1
   THEN 'ATTACHED_EXACT'
  ELSE 'DRIFT' END state,index_oid
 FROM (VALUES(1)) seed(dummy) LEFT JOIN target ON true
)
SELECT state candidate_state,state='ABSENT' state_absent,state='INVALID_EXACT' state_invalid,
 state='READY_UNATTACHED' state_ready,state='ATTACHED_EXACT' state_attached,
 state='PARTIAL_EXACT' state_partial,state='REMOVED_EXACT' state_removed,state='DRIFT' state_drift,
 coalesce(index_oid,0) candidate_oid FROM classified \gset
\echo evidence_candidate=${IDS[$i]} state=:candidate_state oid=:candidate_oid
SQL
}

emit_refuse_bad() {
  cat <<'SQL'
\if :state_invalid
 \echo client_auth_00043=DENY reason=invalid_exact_requires_cleanup
 \quit 78
\endif
\if :state_drift
 \echo client_auth_00043=DENY reason=catalog_drift
 \quit 78
\endif
SQL
}

emit_quality() {
  local i="$1" predicate='' filter
  filter="$(non_null_filter "${COLUMNS[$i]}")"; [[ "$i" -eq 18 ]] && predicate="status='active' AND "
  cat <<SQL
WITH duplicate_groups AS (
 SELECT count(*) n FROM public.${TABLES[$i]} WHERE ${predicate}${filter}
 GROUP BY ${COLUMNS[$i]} HAVING count(*)>1
) SELECT count(*)=0 no_duplicates,count(*) duplicate_groups,coalesce(sum(n),0) duplicate_rows FROM duplicate_groups \gset
\echo evidence_quality=${IDS[$i]} duplicate_groups=:duplicate_groups duplicate_rows=:duplicate_rows
\if :no_duplicates
\else
 \quit 78
\endif
SQL
}

emit_final_evidence() {
  local direction="$1" i filter predicate null_expr terminal
  for i in "${!IDS[@]}"; do
    filter="$(non_null_filter "${COLUMNS[$i]}")"
    null_expr="$(null_profile_expr "${COLUMNS[$i]}")"
    predicate=''; [[ "$i" -eq 18 ]] && predicate="status='active' AND "
    if [[ "$direction" == up ]]; then
      terminal='ATTACHED_EXACT'; [[ "$i" -eq 18 ]] && terminal='PARTIAL_EXACT'
      cat <<SQL
WITH object AS (
 SELECT tc.oid table_oid,ic.oid index_oid,coalesce(con.oid,0) constraint_oid,
  pg_get_indexdef(ic.oid) indexdef,coalesce(pg_get_expr(ix.indpred,ix.indrelid,false),'') predicate,
  coalesce((SELECT string_agg(concat_ws(':',d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.refobjsubid,d.deptype),',' ORDER BY d.classid,d.objid,d.objsubid,d.refclassid,d.refobjid,d.refobjsubid,d.deptype)
    FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=ic.oid),'') dependency
 FROM pg_class tc JOIN pg_namespace n ON n.oid=tc.relnamespace
 JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname='${NAMES[$i]}'
 JOIN pg_index ix ON ix.indexrelid=ic.oid
 LEFT JOIN pg_constraint con ON con.connamespace=n.oid AND con.conname='${NAMES[$i]}' AND con.conrelid=tc.oid
 WHERE n.nspname='public' AND tc.relname='${TABLES[$i]}'
), quality AS (
 SELECT (SELECT count(*) FROM (SELECT 1 FROM public.${TABLES[$i]} WHERE ${predicate}${filter} GROUP BY ${COLUMNS[$i]} HAVING count(*)>1) d) duplicate_groups,
  encode(sha256(convert_to((SELECT ${null_expr}::text FROM public.${TABLES[$i]}),'UTF8')),'hex') null_sha
)
SELECT 'candidate=${IDS[$i]}|table=${TABLES[$i]}|table_oid='||table_oid||
 '|index_oid='||index_oid||'|constraint_oid='||constraint_oid||
 '|pre_state=${terminal}|action=VERIFY|post_state=${terminal}|indexdef_sha256='||
 encode(sha256(convert_to(indexdef,'UTF8')),'hex')||'|predicate_sha256='||
 encode(sha256(convert_to(predicate,'UTF8')),'hex')||'|dependency_sha256='||
 encode(sha256(convert_to(dependency,'UTF8')),'hex')||'|duplicate_groups='||
 duplicate_groups||'|null_profile_sha256='||null_sha FROM object CROSS JOIN quality;
SQL
    else
      cat <<SQL
WITH parent AS (
 SELECT tc.oid table_oid FROM pg_class tc JOIN pg_namespace n ON n.oid=tc.relnamespace
 WHERE n.nspname='public' AND tc.relname='${TABLES[$i]}' AND tc.relkind='r' AND tc.relpersistence='p'
), quality AS (
 SELECT (SELECT count(*) FROM (SELECT 1 FROM public.${TABLES[$i]} WHERE ${predicate}${filter} GROUP BY ${COLUMNS[$i]} HAVING count(*)>1) d) duplicate_groups,
  encode(sha256(convert_to((SELECT ${null_expr}::text FROM public.${TABLES[$i]}),'UTF8')),'hex') null_sha
)
SELECT 'candidate=${IDS[$i]}|table=${TABLES[$i]}|table_oid='||table_oid||
 '|index_oid=0|constraint_oid=0|pre_state=REMOVED_EXACT|action=VERIFY|post_state=REMOVED_EXACT|indexdef_sha256='||
 encode(sha256(''::bytea),'hex')||'|predicate_sha256='||encode(sha256(''::bytea),'hex')||
 '|dependency_sha256='||encode(sha256(''::bytea),'hex')||'|duplicate_groups='||
 duplicate_groups||'|null_profile_sha256='||null_sha FROM parent CROSS JOIN quality;
SQL
    fi
  done
  cat <<'SQL'
SELECT 'protected_surface_sha256='||encode(sha256(convert_to(catalog_manifest::text,'UTF8')),'hex')
 FROM app.client_auth_00042_meta WHERE singleton;
SELECT 'allowlist_sha256='||encode(sha256(convert_to('CLIENT-AUTH-00043-U43-01..U43-19-v3','UTF8')),'hex');
SELECT 'runtime_rows='||(
 (SELECT count(*) FROM public.config_bundle_nodes)+(SELECT count(*) FROM public.refresh_families)+
 (SELECT count(*) FROM public.device_proof_nonces)+(SELECT count(*) FROM public.client_access_token_jtis)+
 (SELECT count(*) FROM public.device_issuance_response_replays)+(SELECT count(*) FROM public.client_refresh_response_replays)+
 (SELECT count(*) FROM public.device_issuance_replay_uses)+(SELECT count(*) FROM public.client_refresh_replay_uses));
SQL
}

emit_up() {
  local i
  emit_header up
  for i in "${!IDS[@]}"; do
    emit_boundary up; emit_classify "$i"; emit_refuse_bad
    cat <<'SQL'
\if :state_absent
SQL
    emit_quality "$i"
    printf "SET statement_timeout='%s';\n" "${CIC_TIMEOUT:-__REQUIRED__}"
    if [[ "$i" -eq 18 ]]; then
      printf "CREATE UNIQUE INDEX CONCURRENTLY %s ON public.%s USING btree (%s) WHERE status='active';\n" "${NAMES[$i]}" "${TABLES[$i]}" "${COLUMNS[$i]}"
    else
      printf 'CREATE UNIQUE INDEX CONCURRENTLY %s ON public.%s USING btree (%s);\n' "${NAMES[$i]}" "${TABLES[$i]}" "${COLUMNS[$i]}"
    fi
    cat <<'SQL'
SET statement_timeout='30s';
\endif
SQL
    emit_classify "$i"; emit_refuse_bad
  done
  for i in {0..17}; do
    emit_boundary up; emit_classify "$i"; emit_refuse_bad
    cat <<'SQL'
\if :state_ready
SQL
    emit_quality "$i"
    printf 'ALTER TABLE public.%s ADD CONSTRAINT %s UNIQUE USING INDEX %s;\n' "${TABLES[$i]}" "${NAMES[$i]}" "${NAMES[$i]}"
    cat <<'SQL'
\endif
SQL
    emit_classify "$i"
    cat <<'SQL'
\if :state_attached
\else
 \quit 78
\endif
SQL
  done
  emit_classify 18
  cat <<'SQL'
\if :state_partial
\else
 \quit 78
\endif
SQL
  emit_final_evidence up
  cat <<'SQL'
SELECT pg_advisory_unlock(420042,1) advisory_unlock_ok \gset
\if :advisory_unlock_ok
\else
 \quit 78
\endif
SQL
}

emit_down() {
  local i
  emit_header down
  for ((i=17;i>=0;i--)); do
    emit_boundary down; emit_classify "$i" down; emit_refuse_bad
    cat <<'SQL'
\if :state_attached
SQL
    printf 'ALTER TABLE public.%s DROP CONSTRAINT %s RESTRICT;\n' "${TABLES[$i]}" "${NAMES[$i]}"
    cat <<'SQL'
\endif
SQL
    emit_classify "$i" down
    cat <<'SQL'
\if :state_removed
\else
 \quit 78
\endif
SQL
  done
  emit_boundary down; emit_classify 18 down; emit_refuse_bad
  cat <<'SQL'
\if :state_partial
 DROP INDEX CONCURRENTLY public.refresh_tokens_one_active_family RESTRICT;
\endif
SQL
  emit_classify 18 down
  cat <<'SQL'
\if :state_removed
\else
 \quit 78
\endif
SQL
  emit_final_evidence down
  cat <<'SQL'
SELECT pg_advisory_unlock(420042,1) advisory_unlock_ok \gset
\if :advisory_unlock_ok
\else
 \quit 78
\endif
SQL
}

emit_status() {
  emit_header up
  local i; for i in "${!IDS[@]}"; do emit_classify "$i"; done
  printf 'SELECT pg_advisory_unlock(420042,1);\n'
}

emit_cleanup() {
  local i="$1" def; def="$(expected_def "$i")"
  emit_header up; emit_boundary up
  cat <<SQL
\unset cleanup_exact cleanup_oid
WITH target AS (
 SELECT ic.oid,tc.oid table_oid
 FROM (VALUES(1)) seed(dummy)
 LEFT JOIN pg_namespace n ON n.nspname='public'
 LEFT JOIN pg_class tc ON tc.relnamespace=n.oid AND tc.relname='${TABLES[$i]}'
 LEFT JOIN pg_class ic ON ic.relnamespace=n.oid AND ic.relname='${NAMES[$i]}'
 LEFT JOIN pg_index ix ON ix.indexrelid=ic.oid
 LEFT JOIN pg_am am ON am.oid=ic.relam
 WHERE ic.oid=${EXPECTED_OID}::oid AND ic.relkind='i' AND ic.relowner=tc.relowner
  AND ic.relpersistence='p' AND ic.reltablespace=0 AND ic.reloptions IS NULL
  AND am.amname='btree' AND ix.indisunique AND ix.indimmediate
  AND NOT ix.indnullsnotdistinct AND ix.indexprs IS NULL
  AND pg_get_indexdef(ic.oid)='${def}'
  AND (NOT ix.indisvalid OR NOT ix.indisready OR NOT ix.indislive)
  AND NOT EXISTS(SELECT 1 FROM pg_constraint con WHERE con.conindid=ic.oid)
  AND NOT EXISTS(
    SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=ic.oid
     AND NOT (d.refclassid='pg_class'::regclass AND d.refobjid=tc.oid)
  )
) SELECT count(*)=1 cleanup_exact,coalesce(min(oid),0) cleanup_oid FROM target \gset
\if :cleanup_exact
 DROP INDEX CONCURRENTLY public.${NAMES[$i]} RESTRICT;
\else
 \quit 78
\endif
SELECT CASE WHEN to_regclass('public.${NAMES[$i]}') IS NULL THEN 1 ELSE 1/0 END;
SELECT pg_advisory_unlock(420042,1);
SQL
}

validate_evidence_v2() {
  local file="$1" direction="$2" prior="$3" i line expected_terminal
  [[ -f "$file" && ! -L "$file" && "$(stat -c '%u:%a:%h' "$file")" == '0:600:1' ]] || deny evidence_metadata_invalid
  LC_ALL=C grep -q $'\r' "$file" && deny evidence_cr_forbidden
  od -An -tx1 "$file" | LC_ALL=C grep -qw 00 && deny evidence_nul_forbidden
  mapfile -t evidence_lines <"$file" || deny evidence_read_failed
  [[ "${#evidence_lines[@]}" -eq 40 ]] || deny evidence_line_count_invalid
  LC_ALL=C grep -n '[^ -~]' "$file" >/dev/null && deny evidence_non_ascii_or_invalid_utf8
  for line in "${evidence_lines[@]}"; do ((${#line} <= 4096)) || deny evidence_line_too_long; done
  local -a headers=(format direction run_id source_system_identifier database_name database_oid release_manifest_sha256 runner_sha256 migration_sha256 goose_sha256 psql_sha256 migrations_inventory_sha256 advisory_key started_at_epoch pre_goose_waterline prior_up_evidence_sha256)
  for i in "${!headers[@]}"; do
    [[ "${evidence_lines[$i]}" == "${headers[$i]}="* && ${#evidence_lines[$i]} -le 4096 ]] || deny "evidence_header_order_invalid:${headers[$i]}"
  done
  [[ "${evidence_lines[0]}" == format=client-auth-00043-evidence-v2 &&
     "${evidence_lines[1]}" == "direction=$direction" &&
     "${evidence_lines[2]}" == "run_id=$RUN_ID" &&
     "${evidence_lines[3]}" == "source_system_identifier=$EXPECTED_SYSTEM" &&
     "${evidence_lines[4]}" == "database_name=$EXPECTED_DB_NAME" &&
     "${evidence_lines[5]}" == "database_oid=$EXPECTED_DB_OID" &&
     "${evidence_lines[6]}" == "release_manifest_sha256=$RELEASE_MANIFEST_SHA" &&
     "${evidence_lines[7]}" == "runner_sha256=$RUNNER_SHA" &&
     "${evidence_lines[8]}" == "migration_sha256=$MIGRATION_SHA" &&
     "${evidence_lines[9]}" == "goose_sha256=${RELEASE[goose_sha256]}" &&
     "${evidence_lines[10]}" == "psql_sha256=${RELEASE[psql_sha256]}" &&
     "${evidence_lines[11]}" == "migrations_inventory_sha256=${RELEASE[inventory_sha256]}" &&
     "${evidence_lines[12]}" == advisory_key=420042,1 &&
     "${evidence_lines[15]}" == "prior_up_evidence_sha256=$prior" ]] || deny evidence_identity_mismatch
  [[ "${evidence_lines[13]}" =~ ^started_at_epoch=[1-9][0-9]{0,19}$ ]] || deny evidence_started_at_invalid
  [[ "${evidence_lines[14]}" == "pre_goose_waterline=$([[ "$direction" == up ]] && printf 42 || printf 43)" ]] || deny evidence_waterline_invalid
  for i in {0..18}; do
    line="${evidence_lines[$((16+i))]}"
    expected_terminal=ATTACHED_EXACT
    [[ "$i" -eq 18 ]] && expected_terminal=PARTIAL_EXACT
    [[ "$direction" == down ]] && expected_terminal=REMOVED_EXACT
    if [[ "$direction" == down ]]; then
      oid_pattern='index_oid=0\|constraint_oid=0'
    elif [[ "$i" -eq 18 ]]; then
      oid_pattern='index_oid=[1-9][0-9]*\|constraint_oid=0'
    else
      oid_pattern='index_oid=[1-9][0-9]*\|constraint_oid=[1-9][0-9]*'
    fi
    [[ "$line" =~ ^candidate=${IDS[$i]}\|table=${TABLES[$i]}\|table_oid=[1-9][0-9]*\|${oid_pattern}\|pre_state=${expected_terminal}\|action=VERIFY\|post_state=${expected_terminal}\|indexdef_sha256=[0-9a-f]{64}\|predicate_sha256=[0-9a-f]{64}\|dependency_sha256=[0-9a-f]{64}\|duplicate_groups=0\|null_profile_sha256=[0-9a-f]{64}$ ]] || deny "evidence_candidate_invalid:${IDS[$i]}"
  done
  [[ "${evidence_lines[35]}" =~ ^protected_surface_sha256=[0-9a-f]{64}$ &&
     "${evidence_lines[36]}" =~ ^allowlist_sha256=[0-9a-f]{64}$ &&
     "${evidence_lines[37]}" == runtime_rows=0 &&
     "${evidence_lines[38]}" == interruption=false &&
     "${evidence_lines[39]}" == status=complete ]] || deny evidence_trailer_invalid
}

run_psql_and_publish() {
  local direction="$1" sql_file raw evidence_dir stage output prior started status
  output="$EVIDENCE_OUT"; prior=none
  if [[ "$direction" == down ]]; then
    output="$DOWN_EVIDENCE_OUT"; prior="$(envv PRIOR_UP_EVIDENCE_SHA256)"
    [[ "$prior" =~ ^[0-9a-f]{64}$ ]] || deny prior_up_evidence_sha_invalid
  fi
  [[ -n "$output" && "$output" == /* && ! -e "$output" && ! -L "$output" ]] || deny evidence_out_must_be_new_absolute_path
  evidence_dir="$(dirname "$output")"
  [[ -d "$evidence_dir" && ! -L "$evidence_dir" ]] || deny evidence_directory_invalid
  sql_file="$(mktemp "${TMPDIR:-/tmp}/client-auth-00043.XXXXXX.sql")"; raw="$(mktemp "${TMPDIR:-/tmp}/client-auth-00043.XXXXXX.raw")"
  trap 'rm -f -- "${sql_file:-}" "${raw:-}" "${stage:-}"' EXIT
  if [[ "$direction" == up ]]; then emit_up >"$sql_file"; else emit_down >"$sql_file"; fi
  started="$(date +%s)"
  set +e
  env -i PATH=/usr/bin:/bin HOME=/nonexistent PGPASSFILE="$PGPASS_FILE" \
    "$PSQL_BIN" -X --no-psqlrc --set=ON_ERROR_STOP=1 --dbname="$DB_URL" --file="$sql_file" >"$raw" 2>&1
  status=$?
  set -e
  [[ "$status" -eq 0 ]] || deny pinned_psql_interrupted
  mapfile -t candidate_lines < <(LC_ALL=C grep '^candidate=' "$raw")
  [[ "${#candidate_lines[@]}" -eq 19 ]] || deny evidence_candidate_count_invalid
  mapfile -t protected_lines < <(LC_ALL=C grep -E '^(protected_surface_sha256|allowlist_sha256|runtime_rows)=' "$raw")
  [[ "${#protected_lines[@]}" -eq 3 ]] || deny evidence_trailer_source_count_invalid
  stage="$(mktemp "$evidence_dir/.client-auth-00043.XXXXXX")"
  {
    printf 'format=client-auth-00043-evidence-v2\n'
    printf 'direction=%s\n' "$direction"
    printf 'run_id=%s\n' "$RUN_ID"
    printf 'source_system_identifier=%s\n' "$EXPECTED_SYSTEM"
    printf 'database_name=%s\n' "$EXPECTED_DB_NAME"
    printf 'database_oid=%s\n' "$EXPECTED_DB_OID"
    printf 'release_manifest_sha256=%s\n' "$RELEASE_MANIFEST_SHA"
    printf 'runner_sha256=%s\n' "$RUNNER_SHA"
    printf 'migration_sha256=%s\n' "$MIGRATION_SHA"
    printf 'goose_sha256=%s\n' "${RELEASE[goose_sha256]}"
    printf 'psql_sha256=%s\n' "${RELEASE[psql_sha256]}"
    printf 'migrations_inventory_sha256=%s\n' "${RELEASE[inventory_sha256]}"
    printf 'advisory_key=420042,1\n'
    printf 'started_at_epoch=%s\n' "$started"
    printf 'pre_goose_waterline=%s\n' "$([[ "$direction" == up ]] && printf 42 || printf 43)"
    printf 'prior_up_evidence_sha256=%s\n' "$prior"
    printf '%s\n' "${candidate_lines[@]}"
    printf '%s\n' "${protected_lines[@]}"
    printf 'interruption=false\n'
    printf 'status=complete\n'
  } >"$stage"
  chmod 0600 "$stage"
  validate_evidence_v2 "$stage" "$direction" "$prior"
  sync -f "$stage" || deny evidence_file_fsync_failed
  ln -- "$stage" "$output" || deny evidence_no_clobber_publish_failed
  sync -f "$evidence_dir" || deny evidence_directory_fsync_failed
  rm -f "$stage" "$sql_file" "$raw"; trap - EXIT
  printf 'client_auth_00043=PASS direction=%s evidence=%s\n' "$direction" "$output"
}

finalize() {
  local direction="$1" goose_cmd approval_options evidence_sha evidence_file prior
  evidence_file="$EVIDENCE_OUT"; prior=none
  [[ "$direction" == down ]] && { evidence_file="$DOWN_EVIDENCE_OUT"; prior="$(envv PRIOR_UP_EVIDENCE_SHA256)"; }
  [[ "$direction" == up || "$prior" =~ ^[0-9a-f]{64}$ ]] || deny prior_up_evidence_sha_invalid
  validate_evidence_v2 "$evidence_file" "$direction" "$prior"
  evidence_sha="$(sha_file "$evidence_file")"
  if [[ "$direction" == up ]]; then
    goose_cmd=(up-to 43)
    approval_options="-c aegis.client_auth_00043_finalize_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=$RUN_ID -c aegis.client_auth_00043_evidence_sha256=$evidence_sha -c aegis.client_auth_00043_runner_sha256=$RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=$EXPECTED_SYSTEM -c aegis.client_auth_00043_source_database_name=$EXPECTED_DB_NAME -c aegis.client_auth_00043_source_database_oid=$EXPECTED_DB_OID"
  else
    goose_cmd=(down-to 42)
    approval_options="-c aegis.client_auth_00043_downgrade_approved=approved-v1 -c aegis.client_auth_00043_writers_stopped=stopped-v1 -c aegis.client_auth_00043_callers_stopped=stopped-v1 -c aegis.client_auth_00043_run_id=$RUN_ID -c aegis.client_auth_00043_down_evidence_sha256=$evidence_sha -c aegis.client_auth_00043_prior_up_evidence_sha256=$prior -c aegis.client_auth_00043_runner_sha256=$RUNNER_SHA -c aegis.client_auth_00043_migration_sha256=$MIGRATION_SHA -c aegis.client_auth_00043_release_id=$RELEASE_ID -c aegis.client_auth_00043_source_system_identifier=$EXPECTED_SYSTEM -c aegis.client_auth_00043_source_database_name=$EXPECTED_DB_NAME -c aegis.client_auth_00043_source_database_oid=$EXPECTED_DB_OID"
  fi
  env -i PATH=/usr/bin:/bin HOME=/nonexistent PGPASSFILE="$PGPASS_FILE" PGOPTIONS="$approval_options" \
    GOOSE_DRIVER=postgres GOOSE_DBSTRING="$DB_URL" "$GOOSE_BIN" -dir "$MIGRATIONS_DIR" "${goose_cmd[@]}" \
    || deny goose_finalize_failed
  validate_root_artifact "$POST_GOOSE_VERIFIER"
  [[ "$(sha_file "$POST_GOOSE_VERIFIER")" == "${RELEASE[post_goose_verifier_sha256]}" ]] \
    || deny post_goose_verifier_changed_after_goose
  env -i PATH=/usr/bin:/bin HOME=/nonexistent PGPASSFILE="$PGPASS_FILE" \
    "$PSQL_BIN" -X --no-psqlrc --set=ON_ERROR_STOP=1 \
    --set="verify_direction=$direction" \
    --set="expected_source_system_identifier=$EXPECTED_SYSTEM" \
    --set="expected_database_name=$EXPECTED_DB_NAME" \
    --set="expected_database_oid=$EXPECTED_DB_OID" \
    --set="expected_release_id=$RELEASE_ID" \
    --set="expected_runner_sha256=$RUNNER_SHA" \
    --set="expected_migration_sha256=$MIGRATION_SHA" \
    --set="expected_run_id=$RUN_ID" \
    --set="expected_evidence_sha256=$evidence_sha" \
    --set="future_gate_contract=$FUTURE_GATE_CONTRACT" \
    --dbname="$DB_URL" --file="$POST_GOOSE_VERIFIER" >/dev/null \
    || deny goose_exact_postcheck_failed
}

case "$COMMAND" in
  plan-up) emit_up ;;
  plan-down) emit_down ;;
  status)
    [[ -n "$DB_URL" ]] || deny database_url_required
    PSQL_BIN="$(envv PSQL_BIN)"; [[ -x "$PSQL_BIN" ]] || deny psql_bin_required
    emit_status | env -i PATH=/usr/bin:/bin HOME=/nonexistent PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X --no-psqlrc --dbname="$DB_URL" --file=-
    ;;
  execute-up|resume-up) run_psql_and_publish up ;;
  execute-down|resume-down) run_psql_and_publish down ;;
  cleanup-invalid)
    index=''
    for i in "${!NAMES[@]}"; do [[ "${NAMES[$i]}" == "$CLEANUP_NAME" ]] && index="$i"; done
    [[ -n "$index" ]] || deny cleanup_name_not_allowlisted
    CATALOG_FILE="$(envv CATALOG_EVIDENCE_FILE)"
    JOURNAL_FILE="$(envv CLEANUP_JOURNAL_FILE)"
    [[ -f "$CATALOG_FILE" && ! -L "$CATALOG_FILE" && "$(sha_file "$CATALOG_FILE")" == "$CATALOG_SHA" ]] || deny catalog_evidence_mismatch
    [[ -f "$JOURNAL_FILE" && ! -L "$JOURNAL_FILE" && "$(stat -c '%u:%a:%h' "$JOURNAL_FILE")" == '0:600:1' ]] || deny cleanup_journal_metadata_invalid
    mapfile -t journal_lines <"$JOURNAL_FILE" || deny cleanup_journal_read_failed
    [[ "${#journal_lines[@]}" -eq 10 &&
       "${journal_lines[0]}" == format=client-auth-00043-cleanup-journal-v1 &&
       "${journal_lines[1]}" == "run_id=$RUN_ID" &&
       "${journal_lines[2]}" == "source_system_identifier=$EXPECTED_SYSTEM" &&
       "${journal_lines[3]}" == "database_name=$EXPECTED_DB_NAME" &&
       "${journal_lines[4]}" == "database_oid=$EXPECTED_DB_OID" &&
       "${journal_lines[5]}" == "candidate=${IDS[$index]}" &&
       "${journal_lines[6]}" == "name=$CLEANUP_NAME" &&
       "${journal_lines[7]}" == "index_oid=$EXPECTED_OID" &&
       "${journal_lines[8]}" == "catalog_sha256=$CATALOG_SHA" &&
       "${journal_lines[9]}" == status=complete ]] || deny cleanup_journal_invalid
    token="DROP_EXACT_INVALID:${CLEANUP_NAME}:${EXPECTED_OID}:${CATALOG_SHA}:${MIGRATION_SHA}:${RUNNER_SHA}:${RUN_ID}"
    [[ "$(envv INVALID_CLEANUP_CONFIRM)" == "$token" ]] || deny invalid_cleanup_confirmation_mismatch
    sql_file="$(mktemp "${TMPDIR:-/tmp}/client-auth-00043-cleanup.XXXXXX.sql")"; trap 'rm -f -- "$sql_file"' EXIT
    emit_cleanup "$index" >"$sql_file"
    env -i PATH=/usr/bin:/bin HOME=/nonexistent PGPASSFILE="$PGPASS_FILE" "$PSQL_BIN" -X --no-psqlrc --set=ON_ERROR_STOP=1 --dbname="$DB_URL" --file="$sql_file" || deny invalid_cleanup_failed
    ;;
  finalize-up) finalize up ;;
  finalize-down) finalize down ;;
esac
