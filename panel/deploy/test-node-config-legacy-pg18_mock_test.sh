#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RUNNER="$ROOT/deploy/test-node-config-legacy-pg18.sh"

bash -n "$RUNNER"
grep -Fq 'POSTGRES_PASSWORD_FILE=/run/pandora-secrets/postgres_password' "$RUNNER"
grep -Fq -- '--pull=never' "$RUNNER"
grep -Fq 'docker network create --internal' "$RUNNER"
grep -Fq 'GOPROXY=off' "$RUNNER"
grep -Fq 'GOSUMDB=off' "$RUNNER"
grep -Fq 'PANDORA_TEST_GO_MODCACHE_MANIFEST' "$RUNNER"
grep -Fq 'PANDORA_TEST_GOOSE_SHA256' "$RUNNER"
grep -Fq '[[ "$GOOSE_BIN_SHA256" == "$GOOSE_SHA256_EXPECTED" ]]' "$RUNNER"
grep -Fq '[[ ! -L "$GO_MODCACHE_MANIFEST" ]]' "$RUNNER"
grep -Fq 'find . ! -type d ! -type f -print -quit' "$RUNNER"
grep -Fq 'write_modcache_inventory "$GO_MODCACHE_REAL"' "$RUNNER"
grep -Fq 'cmp -s "$MODCACHE_EXPECTED_MANIFEST" "$MODCACHE_HOST_INVENTORY"' "$RUNNER"
grep -Fq 'cp -a -- "$GO_MODCACHE_REAL/." "$MODCACHE_SNAPSHOT/"' "$RUNNER"
grep -Fq 'src=$MODCACHE_SNAPSHOT,dst=/gomodcache,readonly' "$RUNNER"
grep -Fq 'cmp -s "$MODCACHE_SNAPSHOT_BEFORE" "$MODCACHE_SNAPSHOT_AFTER"' "$RUNNER"
grep -Fq 'copy_source_snapshot "$SOURCE_BEFORE" "$SOURCE_SNAPSHOT"' "$RUNNER"
grep -Fq 'src=$EXEC_ROOT,dst=/src,readonly' "$RUNNER"
grep -Fq 'cmp -s "$SOURCE_SNAPSHOT_BEFORE" "$SOURCE_SNAPSHOT_AFTER"' "$RUNNER"
grep -Fq "find \"\$source_root/internal\" \"\$source_root/web\" -type f -name '*.go' -print0 >\"\$file_list\"" "$RUNNER"
grep -Fq "docker inspect -f '{{.Image}}'" "$RUNNER"
grep -Fq '"$POSTGRES_IMAGE_ID" >/dev/null' "$RUNNER"
grep -Fq '"$GO_IMAGE_ID" sh -ec' "$RUNNER"
grep -Fq "[[ \"\$(docker inspect -f '{{.Image}}' \"\$PG_CONTAINER\")\" == \"\$POSTGRES_IMAGE_ID\" ]]" "$RUNNER"
grep -Fq "[[ \"\$(docker inspect -f '{{.Image}}' \"\$GO_CONTAINER\")\" == \"\$GO_IMAGE_ID\" ]]" "$RUNNER"
grep -Fq "WORK_DIR_ID=\"\$(stat -c '%d:%i' \"\$WORK_DIR\")\"" "$RUNNER"
grep -Fq 'WORK_OWNER_TOKEN="pandora-nodecfg-owner:$RUN_ID"' "$RUNNER"
grep -Fq 'mount_targets="$(findmnt -rn -o TARGET)"' "$RUNNER"
grep -Fq "[[ \"\$(stat -c '%d:%i' \"\$resolved\")\" == \"\$WORK_DIR_ID\" ]]" "$RUNNER"
grep -Fq "[[ \"\$(cat \"\$WORK_OWNER_MARKER\")\" == \"\$WORK_OWNER_TOKEN\" ]]" "$RUNNER"
grep -Fq 'rm -rf --one-file-system -- "$resolved"' "$RUNNER"
grep -Fq "DSN_SECRET_ERE='postgres(ql)?://" "$RUNNER"
grep -Fq "PRIVATE_KEY_ERE='BEGIN (ENCRYPTED |RSA |DSA |EC |OPENSSH )?PRIVATE KEY'" "$RUNNER"
grep -Fq 'pandora-nodecfg-disposable:' "$RUNNER"
grep -Fq 'AEGIS_NODE_CONFIG_PG18_DATABASE_OID' "$RUNNER"
grep -Fq 'AEGIS_NODE_CONFIG_PG18_SYSTEM_ID' "$RUNNER"
grep -Fq 'pg_label" == "$RUN_ID' "$RUNNER"
grep -Fq 'live_system" == "$SYSTEM_IDENTIFIER' "$RUNNER"
grep -Fq 'live_database" == "$DATABASE_OID|pandora-nodecfg-disposable:$RUN_ID' "$RUNNER"
grep -Fq 'DROP DATABASE \"$DB_NAME\" WITH (FORCE)' "$RUNNER"
grep -Fq 'docker network inspect -f' "$RUNNER"
grep -Fq 'docker volume inspect -f' "$RUNNER"
grep -Fq 'docker inspect "$container" >>"$DOCKER_INSPECT"' "$RUNNER"
grep -Fq 'secret_scan=' "$RUNNER"
grep -Fq 'First publish is deliberately pessimistic' "$RUNNER"
grep -Fq 'blocked_interface=PANDORA_TEST_GOOSE_BIN' "$RUNNER" ||
  grep -Fq 'BLOCKED_INTERFACE=PANDORA_TEST_GOOSE_BIN' "$RUNNER"
grep -Fq 'business_exit=%s' "$RUNNER"
grep -Fq 'cleanup_exit=%s' "$RUNNER"
grep -Fq 'final_exit=%s' "$RUNNER"
grep -Fq 'exact_containers=%s' "$RUNNER"
grep -Fq 'exact_networks=%s' "$RUNNER"
grep -Fq 'exact_volumes=%s' "$RUNNER"
grep -Fq -- '-timeout=1800s' "$RUNNER"
grep -Fq -- '-run "^TestNodeConfig(PG18LockSchedule|LegacyPG18)$"' "$RUNNER"
grep -Fq "echo 'node_config_legacy_pg18_business=ok'" "$RUNNER"
grep -Fq "printf 'node_config_legacy_pg18_full_suite=ok\\n' >&3" "$RUNNER"
for marker in \
  node_config_pg18_publish_concurrency_ok \
  node_config_pg18_projection_consistency_ok \
  node_config_pg18_tenant_publish_isolation_ok \
  node_config_pg18_rls_cross_tenant_ok \
  node_config_pg18_rls_connection_reuse_ok \
  node_config_pg18_report_wrong_scope_ok \
  node_config_pg18_report_superseded_ok \
  node_config_pg18_report_pool_move_ok \
  node_config_pg18_report_legacy_repeat_append_only_ok \
  node_config_pg18_expired_layer_refusal_ok \
  node_config_pg18_expiry_lock_wait_refusal_ok \
  node_config_pg18_duplicate_layer_refusal_ok \
  node_config_pg18_malformed_global_refusal_ok \
  node_config_pg18_publish_advisory_cancel_rollback_ok \
  node_config_pg18_publish_desired_wait_cancel_rollback_ok \
  node_config_pg18_report_wait_cancel_rollback_ok \
  node_config_pg18_life_publish_then_retire_ok \
  node_config_pg18_life_retire_then_publish_ok \
  node_config_pg18_bootstrap_token_name_binding_ok \
  node_config_pg18_bootstrap_legacy_token_refusal_ok \
  node_config_pg18_bootstrap_name_canonical_ok \
  node_config_pg18_bootstrap_disabled_pool_issue_refusal_ok \
  node_config_pg18_bootstrap_disabled_pool_use_refusal_ok \
  node_config_pg18_bootstrap_existing_pool_binding_ok \
  node_config_pg18_bootstrap_terminal_refusal_ok \
  node_config_pg18_terminal_identity_refusal_ok \
  node_config_pg18_life_pool_disable_commit_refusal_ok \
  node_config_pg18_life_pool_disable_rollback_publish_ok \
  node_config_pg18_life_global_publish_then_retire_ok \
  node_config_pg18_life_global_retire_then_publish_ok \
  node_config_pg18_life_pool_publish_then_retire_ok \
  node_config_pg18_life_pool_retire_then_publish_ok \
  node_config_pg18_del01_empty_delete_publish_refusal_ok \
  node_config_pg18_del03_delete_then_publish_ok \
  node_config_pg18_del03_publish_then_delete_ok \
  node_config_pg18_del04_create_delete_serialization_ok \
  node_config_pg18_del04_clone_delete_serialization_ok \
  node_config_pg18_new02_create_publish_serialization_ok \
  node_config_pg18_new03_clone_publish_serialization_ok \
  node_config_pg18_new04_new_bootstrap_publish_serialization_ok \
  node_config_pg18_new04_existing_bootstrap_publish_serialization_ok \
  node_config_pg18_new04_new_bootstrap_cancel_rollback_ok \
  node_config_pg18_new04_existing_bootstrap_cancel_rollback_ok \
  node_config_pg18_lock01_stress_504_ok \
  node_config_pg18_version_limit_ok; do
  [[ "$(grep -Fc "$marker" "$RUNNER")" -eq 1 ]]
done
grep -Fq "grep -Fq 'node_config_legacy_pg18_second_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_third_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_fourth_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_fifth_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_sixth_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_seventh_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_eighth_batch=ok' \"\$LOG\"" "$RUNNER"
grep -Fq "grep -Fq 'node_config_legacy_pg18_ninth_batch=ok' \"\$LOG\"" "$RUNNER"
if grep -Eq -- '(^|[[:space:]])(-p|--publish)([[:space:]=]|$)' "$RUNNER"; then
  echo 'runner exposes a host port' >&2
  exit 1
fi
if grep -Eq -- '(-e|--env)[[:space:]]+POSTGRES_PASSWORD=' "$RUNNER"; then
  echo 'runner places the PostgreSQL password in Docker inspect state' >&2
  exit 1
fi

echo 'node_config_legacy_pg18_runner_static_contract_only=ok'
