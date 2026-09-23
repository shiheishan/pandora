#!/usr/bin/env bash
# Linux/root-only dynamic wiring gate for CLIENT-AUTH-00043.
#
# This test never connects to PostgreSQL. It builds an isolated, root-owned
# install tree and a trusted release manifest around fake psql/goose binaries.
# The real verifier is copied into the isolated tree so drift testing cannot
# modify the checkout.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER_SOURCE="$ROOT/deploy/run-client-auth-00043-indexes.sh"
GENERATOR="$ROOT/deploy/generate-client-auth-00043-release-manifest.sh"
TMP=''

for handoff in \
  client-auth-00043-runner-goose-redesign-20260731.md \
  client-auth-00043-runner-goose-redesign-v3-20260731.md \
  client-auth-00043-index-constraints-plan-20260730.md \
  client-auth-00043-00047-phase-decision-20260731.md; do
  if [[ ! -f "$ROOT/.ai-company/handoffs/$handoff" || -L "$ROOT/.ai-company/handoffs/$handoff" ]]; then
    printf 'client_auth_00043_linux_wiring=NOT_RUN reason=handoff_artifact_missing\n'
    exit 77
  fi
done

fail() {
  printf 'client_auth_00043_linux_wiring=FAIL reason=%s\n' "$1" >&2
  exit 1
}

sha_file() {
  sha256sum -- "$1" | awk '{print tolower($1)}'
}

cleanup() {
  [[ -n "$TMP" ]] || return 0
  case "$TMP" in
    /root/client-auth-00043-linux-wiring.*)
      [[ -d "$TMP" && ! -L "$TMP" ]] && rm -rf -- "$TMP"
      ;;
    *)
      printf 'client_auth_00043_linux_wiring=FAIL reason=unsafe_cleanup_target\n' >&2
      return 1
      ;;
  esac
  return 0
}

command -v uname >/dev/null 2>&1 || fail required_command_missing:uname
if [[ "$(uname -s)" != Linux || "$EUID" -ne 0 ]]; then
  printf 'client_auth_00043_linux_wiring=NOT_RUN reason=linux_root_required kernel=%s euid=%s db=NOT_CONNECTED\n' \
    "$(uname -s)" "$EUID"
  exit 77
fi

for command_name in \
  bash realpath stat sha256sum mktemp install grep sed awk sync \
  cat chmod cmp dirname env find sort wc tr od ln mkdir rm paste tail; do
  command -v "$command_name" >/dev/null 2>&1 || fail "required_command_missing:${command_name}"
done

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) fail unsupported_architecture ;;
esac

[[ -f "$RUNNER_SOURCE" && -f "$GENERATOR" ]] || fail source_files_missing
TMP="$(mktemp -d /root/client-auth-00043-linux-wiring.XXXXXX)"
[[ "$TMP" == /root/client-auth-00043-linux-wiring.* && -d "$TMP" && ! -L "$TMP" ]] \
  || fail temporary_directory_invalid
trap 'cleanup || exit 1' EXIT
trap 'cleanup; exit 130' HUP INT TERM

assert_trusted_directory_chain() {
  local path="$1" metadata mode
  path="$(realpath -e "$path")"
  while :; do
    [[ -d "$path" && ! -L "$path" ]] || fail "ancestor_not_directory:${path}"
    metadata="$(stat -c '%u:%a' -- "$path")"
    [[ "${metadata%%:*}" == 0 ]] || fail "ancestor_not_root_owned:${path}"
    mode="${metadata#*:}"
    (( (8#$mode & 8#022) == 0 )) || fail "ancestor_group_world_writable:${path}"
    [[ "$path" == / ]] && break
    path="$(dirname "$path")"
  done
}
assert_trusted_directory_chain "$TMP"

make_fake_tools() {
  local case_dir="$1"
  mkdir -p "$case_dir/tools"
  cat >"$case_dir/tools/psql" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
tool_dir="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" == --version ]]; then
  printf 'call' >>"$tool_dir/psql.all_calls"
  printf ' %q' "$@" >>"$tool_dir/psql.all_calls"
  printf '\n' >>"$tool_dir/psql.all_calls"
  printf 'psql (PostgreSQL) 18.9-fake\n'
  exit 0
fi
printf 'call' >>"$tool_dir/psql.all_calls"
printf ' %q' "$@" >>"$tool_dir/psql.all_calls"
printf '\n' >>"$tool_dir/psql.all_calls"
tail -n1 "$tool_dir/psql.all_calls" >>"$tool_dir/psql.calls"
if [[ -e "$tool_dir/verifier_nonzero" ]]; then
  exit 51
fi
exit 0
EOF
  cat >"$case_dir/tools/goose" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
tool_dir="$(cd "$(dirname "$0")" && pwd)"
if [[ "${1:-}" == -version ]]; then
  printf 'call' >>"$tool_dir/goose.all_calls"
  printf ' %q' "$@" >>"$tool_dir/goose.all_calls"
  printf '\n' >>"$tool_dir/goose.all_calls"
  printf 'goose version v3.99.0-fake\n'
  exit 0
fi
printf 'call' >>"$tool_dir/goose.all_calls"
printf ' %q' "$@" >>"$tool_dir/goose.all_calls"
printf '\n' >>"$tool_dir/goose.all_calls"
tail -n1 "$tool_dir/goose.all_calls" >>"$tool_dir/goose.calls"
if [[ -e "$tool_dir/mutate_verifier" ]]; then
  printf '\n-- isolated post-goose drift injection\n' \
    >>"$tool_dir/../install/deploy/verify-client-auth-00043-post-goose.sql"
fi
exit 0
EOF
  chmod 0700 "$case_dir/tools/psql" "$case_dir/tools/goose"
}

copy_install_tree() {
  local case_dir="$1" install_root="$case_dir/install"
  mkdir -p "$install_root/deploy" "$install_root/migrations" \
    "$install_root/.ai-company/handoffs" "$case_dir/output"
  install -m 0700 "$RUNNER_SOURCE" \
    "$install_root/deploy/run-client-auth-00043-indexes.sh"
  install -m 0600 "$ROOT/deploy/verify-client-auth-00043-post-goose.sql" \
    "$install_root/deploy/verify-client-auth-00043-post-goose.sql"
  install -m 0600 "$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql" \
    "$install_root/migrations/00042_client_auth_expand.sql"
  install -m 0600 "$ROOT/migrations/frozen-client-auth/00043_client_auth_existing_parent_indexes.sql" \
    "$install_root/migrations/00043_client_auth_existing_parent_indexes.sql"
  local handoff
  for handoff in \
    client-auth-00043-runner-goose-redesign-20260731.md \
    client-auth-00043-runner-goose-redesign-v3-20260731.md \
    client-auth-00043-index-constraints-plan-20260730.md \
    client-auth-00043-00047-phase-decision-20260731.md; do
    install -m 0600 "$ROOT/.ai-company/handoffs/$handoff" \
      "$install_root/.ai-company/handoffs/$handoff"
  done
  : >"$case_dir/pgpass"
  chmod 0600 "$case_dir/pgpass"
  make_fake_tools "$case_dir"
  assert_trusted_directory_chain "$case_dir/tools"
  assert_trusted_directory_chain "$install_root/deploy"
  assert_trusted_directory_chain "$install_root/migrations"
  assert_trusted_directory_chain "$case_dir/output"
}

manifest_value() {
  local manifest="$1" key="$2"
  sed -n "s/^${key}=//p" "$manifest"
}

generate_trusted_manifest() {
  local case_dir="$1" install_root="$case_dir/install"
  local manifest="$case_dir/release.manifest"
  env \
    PANDORA_CLIENT_AUTH_00043_MANIFEST_GENERATE_APPROVED=approved-trusted-release-artifacts-v1 \
    PANDORA_CLIENT_AUTH_00043_RELEASE_ID=client-auth-00043-v3 \
    PANDORA_CLIENT_AUTH_00043_ARCHITECTURE="$ARCH" \
    PANDORA_CLIENT_AUTH_00043_INSTALL_ROOT="$install_root" \
    PANDORA_CLIENT_AUTH_00043_MIGRATIONS_DIR="$install_root/migrations" \
    PANDORA_CLIENT_AUTH_00043_RUNNER_FILE="$install_root/deploy/run-client-auth-00043-indexes.sh" \
    PANDORA_CLIENT_AUTH_00043_MIGRATION_FILE="$install_root/migrations/00043_client_auth_existing_parent_indexes.sql" \
    PANDORA_CLIENT_AUTH_00043_POST_GOOSE_VERIFIER_FILE="$install_root/deploy/verify-client-auth-00043-post-goose.sql" \
    PANDORA_CLIENT_AUTH_00043_PSQL_BIN="$case_dir/tools/psql" \
    PANDORA_CLIENT_AUTH_00043_GOOSE_BIN="$case_dir/tools/goose" \
    bash "$GENERATOR" --generate "$manifest" >"$case_dir/generator.out" 2>&1 \
    || fail trusted_manifest_generation_failed
  [[ "$(stat -c '%u:%a:%h' "$manifest")" == 0:600:1 ]] \
    || fail trusted_manifest_metadata_invalid
  grep -Fxq status=TRUSTED "$manifest" || fail trusted_manifest_status_invalid
}

make_evidence() {
  local case_dir="$1" direction="$2" output="$3" prior="$4"
  local manifest="$case_dir/release.manifest" state table constraint index i
  local zero='0000000000000000000000000000000000000000000000000000000000000000'
  local -a tables=(
    subscriptions devices sessions sessions sessions sessions
    device_authorizations device_authorizations device_authorizations
    device_tokens device_tokens subscription_credentials
    subscription_credentials config_bundles refresh_tokens refresh_tokens
    refresh_tokens refresh_tokens refresh_tokens
  )
  {
    printf 'format=client-auth-00043-evidence-v2\n'
    printf 'direction=%s\n' "$direction"
    printf 'run_id=linux-wiring-%s\n' "$direction"
    printf 'source_system_identifier=9000000000000000043\n'
    printf 'database_name=pandora_ca43_mock\n'
    printf 'database_oid=430043\n'
    printf 'release_manifest_sha256=%s\n' "$(sha_file "$manifest")"
    printf 'runner_sha256=%s\n' \
      "$(manifest_value "$manifest" runner_sha256)"
    printf 'migration_sha256=%s\n' \
      "$(manifest_value "$manifest" migration_00043_sha256)"
    printf 'goose_sha256=%s\n' "$(manifest_value "$manifest" goose_sha256)"
    printf 'psql_sha256=%s\n' "$(manifest_value "$manifest" psql_sha256)"
    printf 'migrations_inventory_sha256=%s\n' \
      "$(manifest_value "$manifest" inventory_sha256)"
    printf 'advisory_key=420042,1\n'
    printf 'started_at_epoch=1700000043\n'
    if [[ "$direction" == up ]]; then
      printf 'pre_goose_waterline=42\n'
    else
      printf 'pre_goose_waterline=43\n'
    fi
    printf 'prior_up_evidence_sha256=%s\n' "$prior"
    for ((i=1; i<=19; i++)); do
      if [[ "$direction" == down ]]; then
        state=REMOVED_EXACT
        index=0
        constraint=0
      elif [[ "$i" -eq 19 ]]; then
        state=PARTIAL_EXACT
        index=$((1000 + i))
        constraint=0
      else
        state=ATTACHED_EXACT
        index=$((1000 + i))
        constraint=$((2000 + i))
      fi
      printf 'candidate=U43-%02d|table=%s|table_oid=%d|index_oid=%d|constraint_oid=%d|pre_state=%s|action=VERIFY|post_state=%s|indexdef_sha256=%s|predicate_sha256=%s|dependency_sha256=%s|duplicate_groups=0|null_profile_sha256=%s\n' \
        "$i" "${tables[$((i - 1))]}" "$((5000 + i))" "$index" "$constraint" \
        "$state" "$state" "$zero" "$zero" "$zero" "$zero"
    done
    printf 'protected_surface_sha256=%s\n' "$zero"
    printf 'allowlist_sha256=%s\n' "$zero"
    printf 'runtime_rows=0\n'
    printf 'interruption=false\n'
    printf 'status=complete\n'
  } >"$output"
  chmod 0600 "$output"
}

run_finalize() {
  local case_dir="$1" direction="$2" evidence="$3" prior="$4"
  local runner="$case_dir/install/deploy/run-client-auth-00043-indexes.sh"
  local -a command=(finalize-up)
  [[ "$direction" == down ]] && command=(finalize-down)
  env \
    PANDORA_CLIENT_AUTH_00043_EXECUTE_APPROVED=approved-isolated-pg18-clone-v1 \
    PANDORA_CLIENT_AUTH_00043_RUN_ID="linux-wiring-${direction}" \
    PANDORA_CLIENT_AUTH_00043_DATABASE_URL=postgresql://ca43@127.0.0.1:1/pandora_ca43_mock \
    PANDORA_CLIENT_AUTH_00043_PGPASS_FILE="$case_dir/pgpass" \
    PANDORA_CLIENT_AUTH_00043_EVIDENCE_OUT="$case_dir/evidence-up" \
    PANDORA_CLIENT_AUTH_00043_DOWN_EVIDENCE_OUT="$case_dir/evidence-down" \
    PANDORA_CLIENT_AUTH_00043_RELEASE_MANIFEST_FILE="$case_dir/release.manifest" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_RELEASE_MANIFEST_SHA256="$(sha_file "$case_dir/release.manifest")" \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_SOURCE_SYSTEM_IDENTIFIER=9000000000000000043 \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_DATABASE_NAME=pandora_ca43_mock \
    PANDORA_CLIENT_AUTH_00043_EXPECTED_DATABASE_OID=430043 \
    PANDORA_CLIENT_AUTH_00043_CIC_STATEMENT_TIMEOUT=120s \
    PANDORA_CLIENT_AUTH_00043_PRIOR_UP_EVIDENCE_SHA256="$prior" \
    bash "$runner" "${command[@]}"
}

expected_goose_call() {
  local case_dir="$1" direction="$2" command='up-to 43'
  [[ "$direction" == down ]] && command='down-to 42'
  printf 'call -dir %s/install/migrations %s' "$case_dir" "$command"
}

expected_psql_call() {
  local case_dir="$1" direction="$2" evidence="$3"
  local runner_sha migration_sha evidence_sha
  runner_sha="$(sha_file "$case_dir/install/deploy/run-client-auth-00043-indexes.sh")"
  migration_sha="$(sha_file "$case_dir/install/migrations/00043_client_auth_existing_parent_indexes.sql")"
  evidence_sha="$(sha_file "$evidence")"
  printf 'call -X --no-psqlrc --set=ON_ERROR_STOP=1 --set=verify_direction=%s --set=expected_source_system_identifier=9000000000000000043 --set=expected_database_name=pandora_ca43_mock --set=expected_database_oid=430043 --set=expected_release_id=client-auth-00043-v3 --set=expected_runner_sha256=%s --set=expected_migration_sha256=%s --set=expected_run_id=linux-wiring-%s --set=expected_evidence_sha256=%s --set=future_gate_contract=client-auth-00043-known-pre00044-v1 --dbname=postgresql://ca43@127.0.0.1:1/pandora_ca43_mock --file=%s/install/deploy/verify-client-auth-00043-post-goose.sql' \
    "$direction" "$runner_sha" "$migration_sha" "$direction" "$evidence_sha" "$case_dir"
}

# Success path: a generated TRUSTED manifest authorizes fake Goose followed by
# the exact pinned verifier. Up and Down must each be invoked exactly once.
success="$TMP/success"
mkdir -p "$success"
copy_install_tree "$success"
generate_trusted_manifest "$success"
make_evidence "$success" up "$success/evidence-up" none
up_sha="$(sha_file "$success/evidence-up")"
make_evidence "$success" down "$success/evidence-down" "$up_sha"
run_finalize "$success" up "$success/evidence-up" none >"$success/up.out" 2>&1 \
  || fail trusted_up_finalize_failed
run_finalize "$success" down "$success/evidence-down" "$up_sha" >"$success/down.out" 2>&1 \
  || fail trusted_down_finalize_failed
mapfile -t goose_calls <"$success/tools/goose.calls"
expected_goose_up="$(expected_goose_call "$success" up)"
expected_goose_down="$(expected_goose_call "$success" down)"
[[ "${#goose_calls[@]}" -eq 2 &&
   "${goose_calls[0]}" == "$expected_goose_up" &&
   "${goose_calls[1]}" == "$expected_goose_down" ]] \
  || fail goose_exact_argv_or_count_invalid
mapfile -t psql_calls <"$success/tools/psql.calls"
[[ "${#psql_calls[@]}" -eq 2 ]] || fail verifier_total_call_count_invalid
expected_psql_up="$(expected_psql_call "$success" up "$success/evidence-up")"
expected_psql_down="$(expected_psql_call "$success" down "$success/evidence-down")"
[[ "${psql_calls[0]}" == "$expected_psql_up" &&
   "${psql_calls[1]}" == "$expected_psql_down" ]] \
  || fail verifier_exact_argv_invalid
mapfile -t success_goose_all <"$success/tools/goose.all_calls"
mapfile -t success_psql_all <"$success/tools/psql.all_calls"
[[ "${#success_goose_all[@]}" -eq 5 &&
   "${success_goose_all[0]}" == 'call -version' &&
   "${success_goose_all[1]}" == 'call -version' &&
   "${success_goose_all[2]}" == "$expected_goose_up" &&
   "${success_goose_all[3]}" == 'call -version' &&
   "${success_goose_all[4]}" == "$expected_goose_down" ]] \
  || fail goose_all_calls_invalid
[[ "${#success_psql_all[@]}" -eq 5 &&
   "${success_psql_all[0]}" == 'call --version' &&
   "${success_psql_all[1]}" == 'call --version' &&
   "${success_psql_all[2]}" == "$expected_psql_up" &&
   "${success_psql_all[3]}" == 'call --version' &&
   "${success_psql_all[4]}" == "$expected_psql_down" ]] \
  || fail psql_all_calls_invalid

# A verifier nonzero exit must fail closed after exactly one Goose invocation.
nonzero="$TMP/nonzero"
mkdir -p "$nonzero"
copy_install_tree "$nonzero"
generate_trusted_manifest "$nonzero"
make_evidence "$nonzero" up "$nonzero/evidence-up" none
: >"$nonzero/tools/verifier_nonzero"
set +e
run_finalize "$nonzero" up "$nonzero/evidence-up" none >"$nonzero/run.out" 2>&1
nonzero_rc=$?
set -e
[[ "$nonzero_rc" -eq 78 ]] || fail verifier_nonzero_not_denied
grep -Fq 'reason=goose_exact_postcheck_failed' "$nonzero/run.out" \
  || fail verifier_nonzero_reason_missing
[[ "$(wc -l <"$nonzero/tools/goose.calls" | tr -d ' ')" -eq 1 &&
   "$(wc -l <"$nonzero/tools/psql.calls" | tr -d ' ')" -eq 1 ]] \
  || fail verifier_nonzero_tool_count_invalid
expected_nonzero_goose="$(expected_goose_call "$nonzero" up)"
expected_nonzero_psql="$(expected_psql_call "$nonzero" up "$nonzero/evidence-up")"
mapfile -t nonzero_goose_all <"$nonzero/tools/goose.all_calls"
mapfile -t nonzero_psql_all <"$nonzero/tools/psql.all_calls"
[[ "${#nonzero_goose_all[@]}" -eq 3 &&
   "${nonzero_goose_all[0]}" == 'call -version' &&
   "${nonzero_goose_all[1]}" == 'call -version' &&
   "${nonzero_goose_all[2]}" == "$expected_nonzero_goose" ]] \
  || fail verifier_nonzero_goose_all_calls_invalid
[[ "${#nonzero_psql_all[@]}" -eq 3 &&
   "${nonzero_psql_all[0]}" == 'call --version' &&
   "${nonzero_psql_all[1]}" == 'call --version' &&
   "${nonzero_psql_all[2]}" == "$expected_nonzero_psql" ]] \
  || fail verifier_nonzero_psql_all_calls_invalid

# Drift injected by fake Goose must be detected before fake psql can execute.
drift="$TMP/drift"
mkdir -p "$drift"
copy_install_tree "$drift"
generate_trusted_manifest "$drift"
make_evidence "$drift" up "$drift/evidence-up" none
: >"$drift/tools/mutate_verifier"
set +e
run_finalize "$drift" up "$drift/evidence-up" none >"$drift/run.out" 2>&1
drift_rc=$?
set -e
[[ "$drift_rc" -eq 78 ]] || fail verifier_drift_not_denied
grep -Fq 'reason=post_goose_verifier_changed_after_goose' "$drift/run.out" \
  || fail verifier_drift_reason_missing
[[ "$(wc -l <"$drift/tools/goose.calls" | tr -d ' ')" -eq 1 ]] \
  || fail verifier_drift_goose_count_invalid
[[ ! -s "$drift/tools/psql.calls" ]] || fail verifier_executed_after_drift
expected_drift_goose="$(expected_goose_call "$drift" up)"
mapfile -t drift_goose_all <"$drift/tools/goose.all_calls"
mapfile -t drift_psql_all <"$drift/tools/psql.all_calls"
[[ "${#drift_goose_all[@]}" -eq 3 &&
   "${drift_goose_all[0]}" == 'call -version' &&
   "${drift_goose_all[1]}" == 'call -version' &&
   "${drift_goose_all[2]}" == "$expected_drift_goose" ]] \
  || fail verifier_drift_goose_all_calls_invalid
[[ "${#drift_psql_all[@]}" -eq 2 &&
   "${drift_psql_all[0]}" == 'call --version' &&
   "${drift_psql_all[1]}" == 'call --version' ]] \
  || fail verifier_drift_psql_all_calls_invalid

# The canonical placeholder manifest is structurally incapable of authorizing
# execution and must be rejected before either fake binary runs.
placeholder="$TMP/placeholder"
mkdir -p "$placeholder"
copy_install_tree "$placeholder"
bash "$GENERATOR" --placeholder "$placeholder/release.manifest" \
  >"$placeholder/generator.out" 2>&1 || fail placeholder_generation_failed
make_evidence "$placeholder" up "$placeholder/evidence-up" none
set +e
run_finalize "$placeholder" up "$placeholder/evidence-up" none \
  >"$placeholder/run.out" 2>&1
placeholder_rc=$?
set -e
[[ "$placeholder_rc" -eq 78 ]] || fail placeholder_not_denied
grep -Fq 'reason=release_manifest_placeholder_no_go' "$placeholder/run.out" \
  || fail placeholder_reason_missing
[[ ! -e "$placeholder/tools/goose.all_calls" && ! -e "$placeholder/tools/psql.all_calls" ]] \
  || fail placeholder_reached_fake_tools

printf 'client_auth_00043_linux_wiring=PASS trusted_manifest=1 fake_db=NOT_CONNECTED up_once=1 down_once=1 verifier_nonzero_denied=1 verifier_drift_denied=1 placeholder_denied=1 arch=%s\n' \
  "$ARCH"
