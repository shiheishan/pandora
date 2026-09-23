#!/usr/bin/env bash
set -Eeuo pipefail
umask 077

ROOT="$(cd "$(dirname "$0")/.." && pwd -P)"
GEN="$ROOT/deploy/generate-client-auth-00042-expected-manifest.sh"
SELF="$(cd "$(dirname "$0")" && pwd -P)/$(basename "$0")"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/pandora-ca42-manifest-test.XXXXXX")"
trap 'rm -rf -- "$TMP"' EXIT HUP INT TERM
mkdir -p "$TMP/bin"
ln -s "$SELF" "$TMP/bin/docker"
ln -s "$SELF" "$TMP/bin/date"

CID='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
IMAGE='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
NID='cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
RUN='pandoraisolatedpg18ABC123-4321'
MIGRATION_SHA='ffaf84b6e73eb0eef6794f5ca72859607b0c4c5bf313d9f7548dae120848dff5'

case "$(basename "$0")" in
  date) printf '%s\n' 2000; exit 0 ;;
  docker)
    [[ "$1" == '--host' && "$2" == 'unix:///var/run/docker.sock' ]] || exit 91
    shift 2
    if [[ "$1" == network && "$2" == inspect ]]; then
      printf '%s|%s|true|isolated-pg18-v1|%s|3000\n' \
        "$NID" "pandora-pg18-preflight-$RUN" "$RUN"
      exit 0
    fi
    if [[ "$1" == inspect ]]; then
      kind='isolated-pg18-v1'; source_kind='trusted-disposable-client-auth-00042-v1'
      [[ "${MOCK_SCENARIO:-pass}" == bad_label ]] && source_kind='production'
      printf '%s|/%s|%s|%s|%s|3000|%s|%s|true|true|%s\n' \
        "$CID" "pandora-pg18-preflight-$RUN" "$IMAGE" "$kind" "$RUN" \
        "$source_kind" "$MIGRATION_SHA" "pandora-pg18-preflight-$RUN"
      exit 0
    fi
    if [[ "$1" == exec ]]; then
      cat >"$MOCK_QUERY"
      [[ "${MOCK_SCENARIO:-pass}" == db_reject ]] && exit 1
      if [[ "${MOCK_SCENARIO:-pass}" == ambiguous ]]; then printf '{}\n{}\n'; exit 0; fi
      printf '%s\n' '{"format": "client-auth-00042-object-manifest-v1", "portable_catalog_manifest": {"format": "client-auth-00042-catalog-v1"}, "source_object_provenance": {"relations": [["public", "devices", 100, 200, 0, 300, "42", 10]], "roles": [["aegis_client_auth_owner", 20, "43"]]}}'
      exit 0
    fi
    exit 92
    ;;
esac

fail() { printf 'client_auth_00042_manifest_mock=FAIL reason=%s\n' "$1" >&2; exit 1; }

run_gen() {
  local scenario="$1" output="$2"
  env -u DOCKER_HOST -u DOCKER_CONTEXT -u DOCKER_TLS_VERIFY -u DOCKER_CERT_PATH \
    PATH="$TMP/bin:$PATH" MOCK_SCENARIO="$scenario" MOCK_QUERY="$TMP/query.sql" \
    AEGIS_EXPECTED_MANIFEST_OUTPUT="$output" \
    AEGIS_MANIFEST_SOURCE_CONTAINER_ID="$CID" \
    AEGIS_MANIFEST_SOURCE_SYSTEM_IDENTIFIER='777777' \
    AEGIS_MANIFEST_SOURCE_DATABASE='aegis' AEGIS_MANIFEST_SOURCE_DATABASE_OID='16384' \
    AEGIS_MANIFEST_SOURCE_DATABASE_USER='postgres' AEGIS_MANIFEST_SOURCE_IMAGE_ID="$IMAGE" \
    AEGIS_MANIFEST_SOURCE_RUN_ID="$RUN" \
    AEGIS_MANIFEST_BARRIER_MODE='exclusive-shared-and-local-catalog-lock-v1' \
    "$GEN"
}

run_gen pass "$TMP/ready.json" >"$TMP/pass.out" 2>"$TMP/pass.err" || fail happy_status
grep -Fq '"status": "READY"' "$TMP/ready.json" || fail ready_missing
grep -Eq '"exact_object_manifest_sha256": "[0-9a-f]{64}"' "$TMP/ready.json" || fail hash_missing
grep -Fq "LOCK TABLE pg_catalog.pg_authid" "$TMP/query.sql" || fail shared_catalog_barrier_missing
grep -Fq "IN SHARE ROW EXCLUSIVE MODE" "$TMP/query.sql" || fail shared_catalog_lock_mode_missing
grep -Fq "IN ACCESS EXCLUSIVE MODE" "$TMP/query.sql" || fail local_catalog_lock_mode_missing
grep -Fq "source_object_provenance" "$TMP/query.sql" || fail object_provenance_missing
grep -Fq "c.xmin::text" "$TMP/query.sql" || fail immutable_relation_provenance_missing
grep -Fq "r.xmin::text" "$TMP/query.sql" || fail immutable_role_provenance_missing
grep -Fq "d.xmin::text" "$TMP/query.sql" || fail immutable_role_comment_provenance_missing

for scenario in bad_label db_reject ambiguous; do
  set +e
  run_gen "$scenario" "$TMP/$scenario.json" >"$TMP/$scenario.out" 2>"$TMP/$scenario.err"
  rc=$?
  set -e
  [[ "$rc" -eq 78 && ! -e "$TMP/$scenario.json" ]] || fail "$scenario:$rc"
done

set +e
env PATH="$TMP/bin:$PATH" DOCKER_HOST='tcp://attacker.invalid:2375' "$GEN" \
  >"$TMP/remote.out" 2>"$TMP/remote.err"
rc=$?
set -e
[[ "$rc" -eq 78 ]] || fail remote_docker_not_denied

cp "$ROOT/migrations/frozen-client-auth/00042_client_auth_expand.sql" "$TMP/00042.sql"
printf '%s\n' '-- tampered' >>"$TMP/00042.sql"
set +e
AEGIS_CLIENT_AUTH_00042_MIGRATION="$TMP/00042.sql" run_gen pass "$TMP/tampered.json" \
  >"$TMP/tampered.out" 2>"$TMP/tampered.err"
rc=$?
set -e
[[ "$rc" -eq 78 && ! -e "$TMP/tampered.json" ]] || fail tampered_migration_not_denied

grep -Fq '"status": "PLACEHOLDER_NO_GO"' "$ROOT/deploy/client-auth-00042-expected-manifest.json" \
  || fail repository_placeholder_not_no_go
printf 'client_auth_00042_manifest_mock=PASS cases=6\n'
