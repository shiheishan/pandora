package nodefabric

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEffectiveReleaseMigrationFreezesImmutableIdentityContract(t *testing.T) {
	path := filepath.Join("..", "..", "..", "migrations", "00057_node_effective_config_releases.sql")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sql := string(body)
	for _, required := range []string{
		"config_source_generation bigint NOT NULL DEFAULT 1",
		"UNIQUE (tenant_id, node_id, generation)",
		"UNIQUE (tenant_id, node_id, id, generation, content_hash)",
		"FOREIGN KEY (tenant_id, id, applied_effective_release_id, applied_effective_generation, applied_effective_hash)",
		"REFERENCES nodes(tenant_id, id) ON DELETE CASCADE",
		"FOREIGN KEY (tenant_id, node_id, effective_release_id, effective_generation, effective_content_hash)",
		"num_nonnulls(applied_effective_release_id, applied_effective_generation, applied_effective_hash) IN (0, 3)",
		"num_nonnulls(effective_release_id, effective_generation, effective_content_hash) = 3",
		"config_id IS NOT NULL AND report_id IS NULL",
		"node_config_apps_effective_report_unique",
		"report_id <> '00000000-0000-0000-0000-000000000000'::uuid",
		"app.enable_tenant_rls('node_effective_config_releases')",
		"app.make_append_only('node_effective_config_releases')",
		"REVOKE UPDATE, DELETE, TRUNCATE ON node_effective_config_releases FROM aegis_app",
		"DEFERRABLE INITIALLY DEFERRED",
		"cannot roll back effective releases while release evidence exists",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("effective-release migration is missing %q", required)
		}
	}
}
