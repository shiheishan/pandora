// [INPUT]: 依赖 pprof.go 的 loadPprofAddrs、PprofAddrEnv，依赖 config.go 的 Load
// [OUTPUT]: 对外提供 pprof 诊断端口配置的单元测试：缺省关闭、只收回环 IP 字面量、端口判重、非回环让 Load 失败
// [POS]: platform/config 的 pprof 配置单测，与 config_test.go 并列；监听行为的测试在 platform/profiling
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package config

import (
	"strings"
	"testing"
)

var testGatewayAddrs = []string{"127.0.0.1:9000", "127.0.0.1:9001", "127.0.0.1:9002", "127.0.0.1:9003"}

func clearPprofEnv(t *testing.T) {
	t.Helper()
	for _, name := range PprofAddrEnv {
		t.Setenv(name, "")
	}
}

func TestPprofIsOffByDefault(t *testing.T) {
	clearPprofEnv(t)
	got, err := loadPprofAddrs(testGatewayAddrs)
	if err != nil {
		t.Fatalf("unset pprof variables must not fail Load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("pprof must stay off when no variable is set, got %v", got)
	}
}

func TestPprofAcceptsLoopbackLiterals(t *testing.T) {
	clearPprofEnv(t)
	t.Setenv("AEGIS_PUBLIC_PPROF_ADDR", " 127.0.0.1:6060 ")
	t.Setenv("AEGIS_ADMIN_PPROF_ADDR", "127.0.0.2:6061")
	t.Setenv("AEGIS_NODE_PPROF_ADDR", "[::1]:6062")
	got, err := loadPprofAddrs(testGatewayAddrs)
	if err != nil {
		t.Fatal(err)
	}
	want := map[Domain]string{
		DomainPublic: "127.0.0.1:6060",
		DomainAdmin:  "127.0.0.2:6061",
		DomainNode:   "[::1]:6062",
	}
	for d, addr := range want {
		if got[d] != addr {
			t.Fatalf("%s pprof addr = %q, want %q", d, got[d], addr)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("pprof addrs = %v, want exactly the three gateways", got)
	}
}

func TestPprofRejectsAnythingButLoopbackLiterals(t *testing.T) {
	for _, raw := range []string{
		"0.0.0.0:6060",      // 全部网卡
		":6060",             // 同上，省略主机
		"[::]:6060",         // IPv6 全部网卡
		"10.0.0.5:6060",     // 内网也不行
		"203.0.113.7:6060",  // 公网
		"localhost:6060",    // 主机名一律不收
		"pprof.local:6060",  // 主机名
		"[fe80::1%lo]:6060", // 带 zone
		"127.0.0.1",         // 缺端口
		"127.0.0.1:0",       // 随机端口
		"127.0.0.1:70000",   // 端口越界
		"127.0.0.1:http",    // 服务名
	} {
		t.Run(raw, func(t *testing.T) {
			clearPprofEnv(t)
			t.Setenv("AEGIS_NODE_PPROF_ADDR", raw)
			_, err := loadPprofAddrs(testGatewayAddrs)
			if err == nil {
				t.Fatalf("%q must be rejected", raw)
			}
			if !strings.Contains(err.Error(), "AEGIS_NODE_PPROF_ADDR") {
				t.Fatalf("error must name the variable: %v", err)
			}
		})
	}
}

func TestPprofPortsMustNotCollide(t *testing.T) {
	t.Run("with a gateway port", func(t *testing.T) {
		clearPprofEnv(t)
		t.Setenv("AEGIS_ADMIN_PPROF_ADDR", "127.0.0.1:9000")
		if _, err := loadPprofAddrs(testGatewayAddrs); err == nil {
			t.Fatal("pprof on the public gateway port must be rejected")
		}
	})
	t.Run("with another pprof port", func(t *testing.T) {
		clearPprofEnv(t)
		t.Setenv("AEGIS_PUBLIC_PPROF_ADDR", "127.0.0.1:6060")
		t.Setenv("AEGIS_NODE_PPROF_ADDR", "[::ffff:127.0.0.1]:6060")
		if _, err := loadPprofAddrs(testGatewayAddrs); err == nil {
			t.Fatal("two gateways on the same pprof port must be rejected")
		}
	})
}

// 拒绝发生在 Load 里、连库之前：网关以「启动失败」退出，而不是带着对外的 pprof 跑起来
func TestLoadRefusesNonLoopbackPprof(t *testing.T) {
	clearPprofEnv(t)
	t.Setenv("AEGIS_ENV", "development")
	t.Setenv("AEGIS_PUBLIC_PPROF_ADDR", "0.0.0.0:6060")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "AEGIS_PUBLIC_PPROF_ADDR") {
		t.Fatalf("Load must refuse a non-loopback pprof address, got %v", err)
	}
}
