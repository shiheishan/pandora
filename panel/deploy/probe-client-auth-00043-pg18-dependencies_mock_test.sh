#!/usr/bin/env bash
# Static/fail-closed gate only. It must never claim that PostgreSQL 18 ran.
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
PROBE="$ROOT/deploy/probe-client-auth-00043-pg18-dependencies.sh"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/ca43-dep-probe-mock.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM

fail() {
  printf 'client_auth_00043_dependency_probe_mock=FAIL reason=%s\n' "$1" >&2
  exit 1
}
contains() { grep -Fq -- "$2" "$1" || fail "missing:$2"; }

[[ -f "$PROBE" && ! -L "$PROBE" ]] || fail probe_missing
bash -n "$PROBE" || fail probe_bash_syntax

for required in \
  "LOCAL_DOCKER_HOST='unix:///var/run/docker.sock'" \
  'approved-local-disposable-pg18-dependency-probe-v1' \
  'remote_docker_environment:' \
  'network create --internal' \
  'postgres_image_id_mismatch' \
  'baseline_contract_mismatch:' \
  'CREATE DATABASE ca43_dependency_probe TEMPLATE aegis_ca43_base' \
  'CREATE UNIQUE INDEX CONCURRENTLY' \
  'ADD CONSTRAINT %s UNIQUE USING INDEX' \
  'pg_identify_object_as_address' \
  "d.deptype='i'" \
  "internal_dependency_not_exact:\$INTERNAL_CHECK" \
  'dependency-portable.tsv' \
  'portable_signature_sha256=' \
  'production_or_remote_target_used=false' \
  'real_pg18=RUN'; do
  contains "$PROBE" "$required"
done

if grep -Eq -- '(^|[[:space:]])(-p|--publish)([[:space:]]|$)' "$PROBE"; then
  fail host_port_publish_forbidden
fi
if grep -Eq 'postgres(ql)?://|DATABASE_URL|PGHOST' "$PROBE"; then
  fail external_database_surface_present
fi

set +e
env -i PATH="$PATH" bash "$PROBE" >"$TMP/refusal.out" 2>&1
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail missing_inputs_not_denied
grep -Fq 'client_auth_00043_dependency_probe=DENY' "$TMP/refusal.out" \
  || fail refusal_marker_missing
if grep -Fq 'client_auth_00043_dependency_probe=PASS' "$TMP/refusal.out"; then
  fail refused_probe_claimed_pass
fi

printf 'client_auth_00043_dependency_probe_mock=PASS scope=static_and_fail_closed real_pg18=NOT_RUN dynamic_signature=NOT_CREATED\n'
