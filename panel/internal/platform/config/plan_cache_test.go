package config

import (
	"strings"
	"testing"
)

func TestPlanCacheModeIsOffByDefault(t *testing.T) {
	clearRuntimeEnv(t)
	r, err := loadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []Domain{DomainPublic, DomainAdmin, DomainNode} {
		if r.DBPlanCacheMode[d] != "" {
			t.Fatalf("%s plan_cache_mode default = %q, want empty (not sent)", d, r.DBPlanCacheMode[d])
		}
	}
	c := &Config{DatabaseURL: "postgres://aegis_app:pw@127.0.0.1:5433/aegis?sslmode=disable", Runtime: r}
	for _, d := range []Domain{DomainPublic, DomainAdmin, DomainNode} {
		if got := c.DatabaseURLFor(d); got != c.DatabaseURL {
			t.Fatalf("%s DSN changed without a switch: %q", d, got)
		}
	}
}

func TestPlanCacheModeIsPerGatewayAndValidated(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("AEGIS_NODE_DB_PLAN_CACHE_MODE", " force_generic_plan ")
	r, err := loadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if r.DBPlanCacheMode[DomainNode] != "force_generic_plan" || r.DBPlanCacheMode[DomainPublic] != "" || r.DBPlanCacheMode[DomainAdmin] != "" {
		t.Fatalf("per-gateway switch leaked: %v", r.DBPlanCacheMode)
	}
	cases := []struct{ dsn, want string }{
		{"postgres://aegis_app:pw@127.0.0.1:5433/aegis?sslmode=disable", "postgres://aegis_app:pw@127.0.0.1:5433/aegis?sslmode=disable&plan_cache_mode=force_generic_plan"},
		{"postgres://aegis_app:pw@/aegis?host=/run/postgresql", "postgres://aegis_app:pw@/aegis?host=/run/postgresql&plan_cache_mode=force_generic_plan"},
		{"postgresql://aegis_app:pw@127.0.0.1/aegis", "postgresql://aegis_app:pw@127.0.0.1/aegis?plan_cache_mode=force_generic_plan"},
		{"host=/run/postgresql user=aegis_app dbname=aegis", "host=/run/postgresql user=aegis_app dbname=aegis plan_cache_mode=force_generic_plan"},
	}
	for _, tc := range cases {
		c := &Config{DatabaseURL: tc.dsn, Runtime: r}
		if got := c.DatabaseURLFor(DomainNode); got != tc.want {
			t.Fatalf("DatabaseURLFor(node) = %q, want %q", got, tc.want)
		}
		if got := c.DatabaseURLFor(DomainPublic); got != tc.dsn {
			t.Fatalf("public DSN changed: %q", got)
		}
	}

	for _, bad := range []string{"generic", "FORCE_GENERIC_PLAN", "force_generic_plan;drop"} {
		clearRuntimeEnv(t)
		t.Setenv("AEGIS_NODE_DB_PLAN_CACHE_MODE", bad)
		if _, err := loadRuntime(); err == nil || !strings.Contains(err.Error(), "AEGIS_NODE_DB_PLAN_CACHE_MODE") {
			t.Fatalf("value %q must be rejected naming the variable, got %v", bad, err)
		}
	}
}

func TestPlanCacheModeRejectsDoubleSetting(t *testing.T) {
	modes := map[Domain]string{DomainNode: "force_custom_plan"}
	if err := checkPlanCacheModes("postgres://u@h/db?plan_cache_mode=auto", modes); err == nil {
		t.Fatal("a DSN that already carries plan_cache_mode plus the env switch must be rejected")
	}
	if err := checkPlanCacheModes("postgres://u@h/db?plan_cache_mode=auto", map[Domain]string{DomainNode: ""}); err != nil {
		t.Fatalf("DSN-only setting must stay valid: %v", err)
	}
	if err := checkPlanCacheModes("postgres://u@h/db", modes); err != nil {
		t.Fatalf("env-only setting must be valid: %v", err)
	}
}
