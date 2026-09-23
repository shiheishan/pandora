package db

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestConfigureAppRoleClosesTemporarySearchPathSurface(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate contract test source")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(root, "deploy", "configure-app-role.sql"))
	if err != nil {
		t.Fatalf("read configure-app-role.sql: %v", err)
	}
	normalized := strings.Join(strings.Fields(string(body)), " ")
	for _, required := range []string{
		"REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC, aegis_app",
		"ALTER ROLE aegis_app IN DATABASE %I SET search_path TO pg_catalog, public, pg_temp",
		"REVOKE CREATE ON SCHEMA public, app FROM PUBLIC, aegis_app",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("configure-app-role.sql is missing %q", required)
		}
	}
	if strings.Contains(normalized, "ALTER ROLE aegis_app SET search_path") {
		t.Fatal("search_path must be database-scoped; cluster-wide ALTER ROLE is forbidden")
	}
}
