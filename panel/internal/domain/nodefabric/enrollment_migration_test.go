package nodefabric

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeEnrollmentMigrationKeepsPendingIdentityOutsideActiveCredentials(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "00059_node_enrollment_transaction.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"CREATE TABLE node_enrollments",
		"begin_request_sha256  bytea NOT NULL CHECK (octet_length(begin_request_sha256) = 32)",
		"runtime_token_hash    bytea NOT NULL CHECK (octet_length(runtime_token_hash) = 32)",
		"config_signing_public_key bytea NOT NULL CHECK (octet_length(config_signing_public_key) = 32)",
		"UNIQUE (tenant_id, request_id)",
		"UNIQUE (bootstrap_token_id, use_ordinal)",
		"ON node_enrollments (tenant_id, node_id) WHERE state = 'pending'",
		"REFERENCES nodes (tenant_id, id) ON DELETE CASCADE",
		"ON DELETE NO ACTION DEFERRABLE INITIALLY DEFERRED",
		"app.guard_node_enrollment()",
		"app.enable_tenant_rls('node_enrollments')",
		"GRANT UPDATE (state, commit_request_sha256, commit_evidence, abort_reason)",
		"REVOKE UPDATE, DELETE, TRUNCATE ON node_enrollments FROM aegis_app",
		"expired node enrollment cannot be committed",
		"node enrollment cannot commit without matching active credentials",
		"SET LOCAL row_security = off",
		"refusing to remove node enrollment evidence",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("node-enrollment migration is missing %q", required)
		}
	}
	if strings.Contains(sql, "ALTER TABLE node_identities") {
		t.Fatal("pending enrollment must not widen the active identity table")
	}
}
