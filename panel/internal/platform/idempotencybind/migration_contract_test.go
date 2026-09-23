package idempotencybind

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSchema39CompleterIsFullTupleBoundCASWithCleanRollback(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "00039_bound_idempotency_success.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	required := []string{
		"SECURITY DEFINER",
		"SET search_path = pg_catalog",
		"session_user<>'aegis_app'",
		"app.current_tenant_id() IS DISTINCT FROM p_tenant_id",
		"app.current_actor_id() IS DISTINCT FROM p_actor_id",
		"k.scope=p_storage_scope",
		"k.scope=app.idempotency_actor_scope(p_base_scope,p_actor_id)",
		"k.request_hash=p_request_hash",
		"k.claim_generation=p_claim_generation",
		"k.locked_until=p_locked_until",
		"k.resource_type=p_resource_type",
		"k.resource_id=p_resource_id",
		"k.response_format='none'",
		"k.response_payload IS NULL",
		"SET status='succeeded'",
		"response_format='bytes'",
		"p_response_code NOT BETWEEN 200 AND 299",
		"octet_length(p_response_payload)>1048576",
		"bound_idempotency_success_00039_usage",
		"Down refused used or unexpected completion state",
		"FROM PUBLIC,aegis_app",
	}
	for _, fragment := range required {
		if !strings.Contains(sql, fragment) {
			t.Fatalf("schema39 missing %q", fragment)
		}
	}
}

func TestRoleConfiguratorRecognizesExactSchema38And39OwnerFunctions(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "configure-app-role.sql")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, required := range []string{
		"app.bind_idempotency_resource",
		"app.complete_bound_idempotency_success",
		"app.bound_idempotency_success_00039_usage",
		"GRANT SELECT (",
		"response_code,response_body,response_format,response_payload",
		"GRANT UPDATE (",
		"TO aegis_idempotency_owner",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("role configurator missing %q", required)
		}
	}
}
