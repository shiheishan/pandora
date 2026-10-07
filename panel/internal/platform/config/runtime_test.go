package config

import (
	"strings"
	"testing"
	"time"
)

func clearRuntimeEnv(t *testing.T) {
	t.Helper()
	for _, k := range DBMaxConnsEnv {
		t.Setenv(k, "")
	}
	t.Setenv(PasswordHashConcurrencyEnv, "")
	t.Setenv(PasswordHashQueueTimeoutEnv, "")
	t.Setenv(NodePullIntervalEnv, "")
}

// 缺省值按 compose 的 max_connections=60 算：3 条超级用户保留 + 11 条维护余量 +
// public 的 1 条 LISTEN + 三个网关各 15 条。改算式要同时改这条测试和注释。
func TestRuntimeDefaultsFitComposeConnectionBudget(t *testing.T) {
	clearRuntimeEnv(t)
	r, err := loadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if r.DBMaxConns[DomainPublic] != 16 || r.DBMaxConns[DomainAdmin] != 15 || r.DBMaxConns[DomainNode] != 15 {
		t.Fatalf("pool defaults = %v, want public 16 / admin 15 / node 15", r.DBMaxConns)
	}
	total := 0
	for _, n := range r.DBMaxConns {
		total += int(n)
	}
	if total+superuserReservedConnections+maintenanceConnections > composeMaxConnections {
		t.Fatalf("gateway pools %d + reserved %d + maintenance %d exceed max_connections %d",
			total, superuserReservedConnections, maintenanceConnections, composeMaxConnections)
	}
	if r.PasswordHashConcurrency != 2 || r.PasswordHashQueueTimeout != 5*time.Second {
		t.Fatalf("password hash defaults = %d / %s", r.PasswordHashConcurrency, r.PasswordHashQueueTimeout)
	}
	if r.NodePullInterval != 15*time.Second {
		t.Fatalf("node pull interval default = %s, want 15s", r.NodePullInterval)
	}
}

func TestRuntimeOverridesArePerGatewayAndBounded(t *testing.T) {
	clearRuntimeEnv(t)
	t.Setenv("AEGIS_NODE_DB_MAX_CONNS", " 24 ")
	t.Setenv(PasswordHashConcurrencyEnv, "4")
	t.Setenv(PasswordHashQueueTimeoutEnv, "3s")
	t.Setenv(NodePullIntervalEnv, "60")
	r, err := loadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if r.DBMaxConns[DomainNode] != 24 || r.DBMaxConns[DomainPublic] != 16 || r.DBMaxConns[DomainAdmin] != 15 {
		t.Fatalf("per-gateway override leaked: %v", r.DBMaxConns)
	}
	if r.PasswordHashConcurrency != 4 || r.PasswordHashQueueTimeout != 3*time.Second {
		t.Fatalf("password hash overrides = %d / %s", r.PasswordHashConcurrency, r.PasswordHashQueueTimeout)
	}
	if r.NodePullInterval != time.Minute {
		t.Fatalf("node pull interval override = %s, want 1m", r.NodePullInterval)
	}

	for _, tc := range []struct{ key, value, want string }{
		{"AEGIS_PUBLIC_DB_MAX_CONNS", "1", "AEGIS_PUBLIC_DB_MAX_CONNS"},
		{"AEGIS_ADMIN_DB_MAX_CONNS", "eight", "AEGIS_ADMIN_DB_MAX_CONNS"},
		{"AEGIS_NODE_DB_MAX_CONNS", "100000", "AEGIS_NODE_DB_MAX_CONNS"},
		{PasswordHashConcurrencyEnv, "0", PasswordHashConcurrencyEnv},
		{PasswordHashConcurrencyEnv, "64", PasswordHashConcurrencyEnv},
		{PasswordHashQueueTimeoutEnv, "0s", PasswordHashQueueTimeoutEnv},
		{PasswordHashQueueTimeoutEnv, "30s", PasswordHashQueueTimeoutEnv},
		{PasswordHashQueueTimeoutEnv, "soon", PasswordHashQueueTimeoutEnv},
		{NodePullIntervalEnv, "4", NodePullIntervalEnv},
		{NodePullIntervalEnv, "301", NodePullIntervalEnv},
		{NodePullIntervalEnv, "15s", NodePullIntervalEnv},
	} {
		clearRuntimeEnv(t)
		t.Setenv(tc.key, tc.value)
		if _, err := loadRuntime(); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s=%q: err=%v, want rejection naming %s", tc.key, tc.value, err, tc.want)
		}
	}
}
