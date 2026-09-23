#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

CID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
IMAGE='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
NID='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
RUN='pandoraisolatedpg18ABC123-4321'
SHA='dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
UPPER='EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE'

MOCK_TOOL="$(basename "$0")"
if [[ "${1:-}" == __mock_tool__ ]]; then
  [[ $# -ge 2 ]] || exit 96
  MOCK_TOOL="$2"
  shift 2
fi

case "$MOCK_TOOL" in
  date) printf '%s\n' 2000; exit 0 ;;
  uname) printf '%s\n' Linux; exit 0 ;;
  id) printf '%s\n' 0; exit 0 ;;
  env|dirname|mktemp|chmod|rm|sha256sum)
    "/usr/bin/$MOCK_TOOL" "$@"
    exit $?
    ;;
  realpath)
    path=''
    while (($#)); do
      [[ "$1" == -e || "$1" == -- ]] && { shift; continue; }
      path="$1"; shift
    done
    [[ "$path" == /* && -d "$path" && ! -L "$path" ]] || exit 90
    printf '%s\n' "$path"
    exit 0
    ;;
  sync)
    [[ "${MOCK_SCENARIO:-pass}" == sync_fail ]] && exit 1
    exit 0
    ;;
  ln)
    [[ "${MOCK_SCENARIO:-pass}" == publish_fail ]] && exit 1
    /usr/bin/ln "$@"
    exit $?
    ;;
  stat)
    fmt=''; path=''
    while (($#)); do
      if [[ "$1" == -c ]]; then fmt="$2"; shift 2; continue; fi
      [[ "$1" == -- ]] && { shift; continue; }
      path="$1"; shift
    done
    [[ -n "$fmt" && -n "$path" ]] || exit 93
    dev="$(/usr/bin/stat -c '%d' -- "$path")" || exit 94
    inode="$(/usr/bin/stat -c '%i' -- "$path")" || exit 94
    if [[ "$fmt" == '%d|%i' ]]; then
      printf '%s|%s\n' "$dev" "$inode"
    elif [[ "$fmt" == '%u|%F|%a|%d|%i' ]]; then
      mode=700
      [[ "${MOCK_SCENARIO:-pass}" == untrusted_ancestor && "$path" == / ]] && mode=777
      printf '0|directory|%s|%s|%s\n' "$mode" "$dev" "$inode"
    elif [[ "$fmt" == '%u|%F|%a' ]]; then
      mode=700
      if [[ -d "$path" ]]; then
        printf '0|directory|%s\n' "$mode"
      else
        [[ "${MOCK_SCENARIO:-pass}" == untrusted_tool ]] && mode=777
        printf '0|regular file|%s\n' "$mode"
      fi
    elif [[ "$fmt" == '%u|%F|%a|%d|%i|%h|%s|%Y' ]]; then
      mode=700
      [[ "${MOCK_SCENARIO:-pass}" == untrusted_tool ]] && mode=777
      printf '0|regular file|%s|%s|%s|%s|%s|%s\n' \
        "$mode" "$dev" "$inode" "$(/usr/bin/stat -c '%h' -- "$path")" \
        "$(/usr/bin/stat -c '%s' -- "$path")" "$(/usr/bin/stat -c '%Y' -- "$path")"
    elif [[ "$fmt" == '%u|%F|%a|%d|%i|%h|%s' ]]; then
      [[ "${MOCK_SCENARIO:-pass}" == cross_device && "$path" == *'.client-auth-pre00044-target.'* ]] &&
        dev=$((dev+1))
      printf '0|regular file|%s|%s|%s|%s|%s\n' \
        "$(/usr/bin/stat -c '%a' -- "$path")" "$dev" "$inode" \
        "$(/usr/bin/stat -c '%h' -- "$path")" "$(/usr/bin/stat -c '%s' -- "$path")"
    else
      exit 95
    fi
    exit 0
    ;;
  docker)
    [[ "$1" == --host && "$2" == unix:///var/run/docker.sock ]] || exit 91
    shift 2
    if [[ "$1" == network && "$2" == inspect && "$4" == *'.Containers'* ]]; then
      printf '%s|%s\n' "$CID" "pandora-pg18-preflight-$RUN"
      [[ "${MOCK_SCENARIO:-pass}" == extra_network_member ]] &&
        printf '%s|attacker\n' 'ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff'
      exit 0
    fi
    if [[ "$1" == network && "$2" == inspect ]]; then
      printf '%s|%s|true|isolated-pg18-v1|%s|3000\n' \
        "$NID" "pandora-pg18-preflight-$RUN" "$RUN"
      exit 0
    fi
    if [[ "$1" == inspect ]]; then
      printf '%s|/%s|%s|isolated-pg18-v1|%s|3000|trusted-disposable-client-auth-00043-pre00044-v1|43|true|true|%s\n' \
        "$CID" "pandora-pg18-preflight-$RUN" "$IMAGE" "$RUN" \
        "pandora-pg18-preflight-$RUN"
      exit 0
    fi
    if [[ "$1" == exec ]]; then
      cat >"$MOCK_QUERY"
      [[ "${MOCK_SCENARIO:-pass}" =~ ^(backdoor|behavior_mismatch)$ ]] && exit 1
      if [[ "${MOCK_SCENARIO:-pass}" == other_database_session ]]; then
        if [[ "$(grep -Fc "WHERE backend_type='client backend' AND pid<>pg_catalog.pg_backend_pid()" "$MOCK_QUERY")" -eq 2 ]] &&
           ! grep -Fq 'datid=' "$MOCK_QUERY"; then
          exit 1
        fi
      fi
      if [[ "${MOCK_SCENARIO:-pass}" == multi_level_ancestor_mismatch ]]; then
        if grep -Fq 'FROM inheritance_partition_relids walk' "$MOCK_QUERY" &&
           grep -Fq 'WHEN inh.inhrelid=walk.oid THEN inh.inhparent' "$MOCK_QUERY" &&
           grep -Fq 'inh.inhparent IN (SELECT oid FROM inheritance_partition_relids)' "$MOCK_QUERY" &&
           grep -Fq "='$SHA'" "$MOCK_QUERY"; then
          exit 1
        fi
      fi
      if [[ "${MOCK_SCENARIO:-pass}" == multi_level_descendant_mismatch ]]; then
        if grep -Fq 'FROM inheritance_partition_relids walk' "$MOCK_QUERY" &&
           grep -Fq 'ELSE inh.inhrelid' "$MOCK_QUERY" &&
           grep -Fq 'inh.inhparent=walk.oid' "$MOCK_QUERY" &&
           grep -Fq 'FROM inheritance_partition_relids b' "$MOCK_QUERY"; then
          exit 1
        fi
      fi
      [[ "${MOCK_SCENARIO:-pass}" == duplicate ]] && {
        printf '{}\n{}\n'
        exit 0
      }
      local_catalog='{"format": "client-auth-00043-pre00044-target-catalog-v1", "status": "CANDIDATE_NOT_RELEASE_APPROVAL", "relations": [{"owner": "owner", "rls": false, "force_rls": false, "comment": null, "acl": [], "columns": [{"identity":"","generated":"","collation":null}], "constraints": [{"definition":"x","validated":true,"deferrable":false,"initially_deferred":false,"dependencies":[]}], "indexes": [{"definition":"x","dependencies":[]}], "policies": [], "triggers": [{"enabled":"O","definition":"x","function":{"prosrc":"BEGIN RETURN NEW; END"},"dependencies":[]}]}], "app_routine_catalog": [{"prosrc":"BEGIN RETURN NEW; END","dependencies":[]}], "routine_dependency_closure": [], "target_trigger_closure": [], "rewrite_rule_catalog": [], "inheritance_partition_catalog": {"edges":[],"relations":[]}, "routine_surface_contract": "CATALOG_DEPENDENCY_EQUALITY_ONLY_NO_SEMANTIC_SQL_PARSING", "app_client_auth_complement": "DENY", "protected_exact_proof_sha256": "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}'
      if [[ "${MOCK_SCENARIO:-pass}" == missing_dimension ]]; then
        local_catalog="${local_catalog//collation/collation_omitted}"
      fi
      if [[ "${MOCK_SCENARIO:-pass}" == fresh_drift ]]; then
        count=0
        [[ -f "$MOCK_COUNTER" ]] && read -r count <"$MOCK_COUNTER"
        count=$((count+1))
        printf '%s\n' "$count" >"$MOCK_COUNTER"
        ((count > 1)) && local_catalog="${local_catalog%?}, \"fresh\":\"drift\"}"
      fi
      printf '%s\n' "$local_catalog"
      exit 0
    fi
    exit 92
    ;;
esac

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
GEN_SOURCE="$ROOT/deploy/generate-client-auth-00043-pre00044-target-manifest.sh"
SELF="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-pre44-target-test.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"
for tool in docker date uname id stat realpath sync ln env dirname mktemp chmod rm sha256sum; do
  printf '#!/usr/bin/env bash\nexec "%s" __mock_tool__ "%s" "$@"\n' \
    "$SELF" "$tool" >"$TMP/bin/$tool"
  chmod 0700 "$TMP/bin/$tool"
done

GEN="$TMP/generate-client-auth-00043-pre00044-target-manifest.instrumented.sh"
awk -v bin="$TMP/bin" '
  /^# PANDORA_PRE00044_FIXED_TOOLS_BEGIN$/ {
    print
    print "readonly BASH_BIN='\''/usr/bin/bash'\''"
    print "readonly DOCKER_BIN='\''" bin "/docker'\''"
    print "readonly SHA256_BIN='\''" bin "/sha256sum'\''"
    print "readonly DATE_BIN='\''" bin "/date'\''"
    print "readonly STAT_BIN='\''" bin "/stat'\''"
    print "readonly REALPATH_BIN='\''" bin "/realpath'\''"
    print "readonly SYNC_BIN='\''" bin "/sync'\''"
    print "readonly UNAME_BIN='\''" bin "/uname'\''"
    print "readonly ID_BIN='\''" bin "/id'\''"
    print "readonly DIRNAME_BIN='\''" bin "/dirname'\''"
    print "readonly MKTEMP_BIN='\''" bin "/mktemp'\''"
    print "readonly CHMOD_BIN='\''" bin "/chmod'\''"
    print "readonly LN_BIN='\''" bin "/ln'\''"
    print "readonly RM_BIN='\''" bin "/rm'\''"
    print "readonly ENV_BIN='\''" bin "/env'\''"
    inside=1
    next
  }
  /^# PANDORA_PRE00044_FIXED_TOOLS_END$/ { inside=0; print; next }
  !inside { print }
' "$GEN_SOURCE" >"$GEN"
chmod 0700 "$GEN"

strip_fixed_tools() {
  awk '
    /^# PANDORA_PRE00044_FIXED_TOOLS_BEGIN$/ { print; inside=1; next }
    /^# PANDORA_PRE00044_FIXED_TOOLS_END$/ { inside=0; print; next }
    !inside { print }
  ' "$1"
}
strip_fixed_tools "$GEN_SOURCE" >"$TMP/source.outside-marker"
strip_fixed_tools "$GEN" >"$TMP/instrumented.outside-marker"
cmp -s "$TMP/source.outside-marker" "$TMP/instrumented.outside-marker" ||
  { printf 'instrumentation escaped fixed-tool marker\n' >&2; exit 97; }
[[ -z "$(find "$TMP/bin" -type l -print -quit)" ]] ||
  { printf 'mock wrapper symlink forbidden\n' >&2; exit 99; }
[[ "$(grep -Fc '# PANDORA_PRE00044_FIXED_TOOLS_BEGIN' "$GEN_SOURCE")" -eq 1 &&
   "$(grep -Fc '# PANDORA_PRE00044_FIXED_TOOLS_END' "$GEN_SOURCE")" -eq 1 &&
   "$(grep -Fc '# PANDORA_PRE00044_FIXED_TOOLS_BEGIN' "$GEN")" -eq 1 &&
   "$(grep -Fc '# PANDORA_PRE00044_FIXED_TOOLS_END' "$GEN")" -eq 1 ]] ||
  { printf 'fixed-tool marker count invalid\n' >&2; exit 98; }

fail() {
  printf 'client_auth_pre00044_target_manifest_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}
run_gen() {
  local scenario="$1" output="$2"
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    PATH="$TMP/bin:$PATH" MOCK_SCENARIO="$scenario" MOCK_QUERY="$TMP/query.sql" \
    MOCK_COUNTER="$TMP/$scenario.counter" \
    AEGIS_PRE00044_TARGET_MANIFEST_OUTPUT="$output" \
    AEGIS_PRE00044_SOURCE_CONTAINER_ID="$CID" \
    AEGIS_PRE00044_SOURCE_SYSTEM_IDENTIFIER=777777 \
    AEGIS_PRE00044_SOURCE_DATABASE=aegis \
    AEGIS_PRE00044_SOURCE_DATABASE_OID=16384 \
    AEGIS_PRE00044_SOURCE_DATABASE_USER=postgres \
    AEGIS_PRE00044_SOURCE_IMAGE_ID="$IMAGE" \
    AEGIS_PRE00044_SOURCE_RUN_ID="$RUN" \
    AEGIS_PRE00044_00042_CONTRACT_SHA256="$UPPER" \
    AEGIS_PRE00044_00042_CATALOG_SHA256="$SHA" \
    AEGIS_PRE00044_00043_CONTRACT_SHA256="$UPPER" \
    AEGIS_PRE00044_00043_RELEASE_ID=client-auth-00043-v3 \
    AEGIS_PRE00044_00043_RUNNER_SHA256="$SHA" \
    AEGIS_PRE00044_00043_MIGRATION_SHA256="$SHA" \
    AEGIS_PRE00044_00043_RUN_ID=run-43 \
    AEGIS_PRE00044_00043_EVIDENCE_SHA256="$SHA" \
    AEGIS_PRE00044_00043_CATALOG_SHA256="$SHA" \
    AEGIS_PRE00044_PROTECTED_EXACT_PROOF_SHA256="$SHA" \
    AEGIS_PRE00044_APP_ROUTINE_CATALOG_SHA256="$SHA" \
    AEGIS_PRE00044_ROUTINE_DEPENDENCY_CLOSURE_SHA256="$SHA" \
    AEGIS_PRE00044_TRIGGER_CLOSURE_SHA256="$SHA" \
    AEGIS_PRE00044_REWRITE_RULE_CATALOG_SHA256="$SHA" \
    AEGIS_PRE00044_INHERITANCE_PARTITION_CATALOG_SHA256="$SHA" \
    "$GEN"
}

if ! run_gen pass "$TMP/pass.json" >"$TMP/pass.out" 2>"$TMP/pass.err"; then
  while IFS= read -r diagnostic; do
    printf 'happy_path_diagnostic=%s\n' "$diagnostic" >&2
  done <"$TMP/pass.err"
  fail happy_path
fi
grep -Fq '"status": "CANDIDATE_NOT_RELEASE_APPROVAL"' "$TMP/pass.json" ||
  fail candidate_status_missing
[[ "$(stat -c '%a' "$TMP/pass.json")" == 600 ]] || fail output_mode_not_0600
grep -Fq 'BEGIN TRANSACTION ISOLATION LEVEL SERIALIZABLE READ ONLY DEFERRABLE' \
  "$TMP/query.sql" || fail transaction_not_read_only
if grep -Eq '(^|[[:space:]])(INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|GRANT|REVOKE)([[:space:]]|$)' \
  "$TMP/query.sql"; then
  fail database_mutation_found
fi
for needle in "pg_identify_object_as_address" "pg_get_constraintdef" "pg_get_indexdef" \
  "pg_get_triggerdef" "pg_get_functiondef" "'prosrc',p.prosrc" \
  "'acl'" "'owner'" "'rls'" "'force_rls'" "'comment'" \
  "'identity'" "'generated'" "'collation'" "'validated'" "'deferrable'" \
  "'initially_deferred'" "'dependencies'" "'enabled'" \
  "client_auth_00042_meta" "client_auth_00043_meta" \
  "c.relname NOT IN ('client_auth_00042_meta','client_auth_00043_meta')" \
  "unknown app.client_auth_* complement object" \
  "protected_exact_proof_sha256" \
  "app_routine_catalog" "routine_dependency_closure" "target_trigger_closure" \
  "rewrite_rule_catalog" "inheritance_partition_catalog" \
  "pg_catalog.pg_rewrite" "pg_catalog.pg_inherits" "a.attnum" \
  "CATALOG_DEPENDENCY_EQUALITY_ONLY_NO_SEMANTIC_SQL_PARSING" \
  "encode(pg_catalog.sha256(pg_catalog.convert_to(a.value::text,'UTF8')),'hex')" \
  "encode(pg_catalog.sha256(pg_catalog.convert_to(d.value::text,'UTF8')),'hex')" \
  "encode(pg_catalog.sha256(pg_catalog.convert_to(tr.value::text,'UTF8')),'hex')" \
  "encode(pg_catalog.sha256(pg_catalog.convert_to(rw.value::text,'UTF8')),'hex')" \
  "encode(pg_catalog.sha256(pg_catalog.convert_to(ip.value::text,'UTF8')),'hex')" \
  "IN ACCESS SHARE MODE" "pg_catalog.pg_stat_activity"; do
  grep -Fq "$needle" "$TMP/query.sql" || fail "query_dimension_missing:$needle"
done
[[ "$(grep -Fc 'pg_catalog.pg_stat_activity' "$TMP/query.sql")" -ge 2 ]] ||
  fail client_session_recheck_missing
[[ "$(grep -Fc "WHERE backend_type='client backend' AND pid<>pg_catalog.pg_backend_pid()" "$TMP/query.sql")" -eq 2 ]] ||
  fail cluster_wide_client_session_guard_missing
if grep -Fq 'datid=' "$TMP/query.sql"; then
  fail client_session_guard_scoped_to_database
fi
for needle in 'inheritance_partition_relids(oid) AS (' \
  'FROM inheritance_partition_relids walk' \
  'ON inh.inhrelid=walk.oid OR inh.inhparent=walk.oid' \
  'WHEN inh.inhrelid=walk.oid THEN inh.inhparent' \
  'ELSE inh.inhrelid' \
  'inh.inhrelid IN (SELECT oid FROM inheritance_partition_relids)' \
  'inh.inhparent IN (SELECT oid FROM inheritance_partition_relids)' \
  'FROM inheritance_partition_relids b' \
  "'parents'" "'children'"; do
  grep -Fq "$needle" "$TMP/query.sql" || fail "inheritance_closure_missing:$needle"
done
grep -Fq 'inspect_network_members' "$GEN_SOURCE" || fail network_membership_gate_missing
grep -Fq 'fresh_catalog_recomputation_mismatch' "$GEN_SOURCE" ||
  fail fresh_catalog_equality_missing
grep -Fq '#!/usr/bin/bash' "$GEN_SOURCE" || fail fixed_interpreter_missing
if grep -Fq 'command -v' "$GEN_SOURCE"; then fail runtime_path_lookup_present; fi
for fixed in /usr/bin/bash /usr/bin/docker /usr/bin/stat /usr/bin/sha256sum /usr/bin/sync /usr/bin/ln; do
  grep -Fq "'$fixed'" "$GEN_SOURCE" || fail "fixed_tool_missing:$fixed"
done

for scenario in missing_dimension duplicate backdoor sync_fail publish_fail cross_device \
  untrusted_ancestor untrusted_tool extra_network_member other_database_session \
  behavior_mismatch fresh_drift multi_level_ancestor_mismatch \
  multi_level_descendant_mismatch; do
  set +e
  run_gen "$scenario" "$TMP/$scenario.json" \
    >"$TMP/$scenario.out" 2>"$TMP/$scenario.err"
  rc=$?
  set -e
  [[ "$rc" -eq 78 && ! -e "$TMP/$scenario.json" ]] || fail "$scenario:$rc"
done

# Output publication is no-clobber and repeated generation must be refused.
set +e
run_gen pass "$TMP/pass.json" >"$TMP/repeat.out" 2>"$TMP/repeat.err"
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail repeated_output_not_denied

grep -Fq '"status": "PLACEHOLDER_NO_GO"' \
  "$ROOT/deploy/client-auth-00043-pre00044-target-manifest.json" ||
  fail repository_placeholder_not_no_go
grep -Fq '"exact_catalog": null' \
  "$ROOT/deploy/client-auth-00043-pre00044-target-manifest.json" ||
  fail repository_placeholder_invented_baseline

grep -Fq 'output_ancestor_chain_changed_before_publish' "$GEN" ||
  fail pre_publish_identity_gate_missing
grep -Fq 'output_ancestor_chain_changed_after_publish' "$GEN" ||
  fail post_publish_identity_gate_missing
grep -Fq 'output_directory_fsync_after_link_failed' "$GEN" ||
  fail link_directory_fsync_missing
grep -Fq 'output_directory_fsync_after_unlink_failed' "$GEN" ||
  fail unlink_directory_fsync_missing
grep -Fq 'hardlink_publish_identity_mismatch' "$GEN" ||
  fail hardlink_identity_gate_missing

printf 'client_auth_pre00044_target_manifest_mock=PASS cases=15 placeholder=PLACEHOLDER_NO_GO publication=pathtrust,same-device,0600,fsync,no-clobber,pre-post-identity catalog=columns,rules,bidirectional-inheritance,partition network=source-only,cluster-wide-no-other-client,fresh-equality\n'
