// [INPUT]: 依赖 naming.go 的命名空间、地址分配与随机量，依赖 options.go 的 labelPattern
// [OUTPUT]: 单测：名字与代码满足面板的字段规则、识别标记稳定、IP 分配确定且不越界、run ID 与口令的形状
// [POS]: tools/loadtest/seed 的命名契约测试，纯函数，不连库不连网
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"net/netip"
	"regexp"
	"strings"
	"testing"
)

// 后台的套餐代码规则（adminops/catalog.go：2-64 位小写字母、数字、下划线或连字符）
var planCodeRule = regexp.MustCompile(`^[a-z0-9_-]{2,64}$`)

func TestNamespaceNamesFollowPanelRules(t *testing.T) {
	ns := newNamespace("15k", "a1b2c3")
	if got := ns.Email(0); got != "lt-15k-a1b2c3-u000001@loadtest.invalid" {
		t.Fatalf("Email(0) = %q", got)
	}
	if got := ns.NodeName(199); got != "loadtest-15k-a1b2c3-n0200" {
		t.Fatalf("NodeName(199) = %q", got)
	}
	if got := ns.ServerName(0); got != "loadtest-15k-a1b2c3-s0001" {
		t.Fatalf("ServerName(0) = %q", got)
	}
	for _, code := range []string{ns.PoolCode(), ns.PlanCode()} {
		if !planCodeRule.MatchString(code) {
			t.Fatalf("code %q breaks the admin code rule", code)
		}
	}
	// 最长的 label 也要放得下：节点名 ≤120、主机名每段 ≤63 且不以连字符起止
	long := newNamespace(strings.Repeat("a", 15)+"9", "ffffff")
	if !labelPattern.MatchString(long.Label) {
		t.Fatal("16-character label should be accepted")
	}
	if n := len(long.NodeName(1499)); n > 120 {
		t.Fatalf("node name length %d > 120", n)
	}
	for _, part := range strings.Split(long.NodeHost(1499), ".") {
		if part == "" || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			t.Fatalf("host label %q is not a valid DNS label", part)
		}
	}
	if !planCodeRule.MatchString(long.PlanCode()) {
		t.Fatalf("long plan code %q breaks the admin code rule", long.PlanCode())
	}
}

func TestLoadtestMarkersIdentifyEverySeededName(t *testing.T) {
	ns := newNamespace("ci", "000001")
	if !strings.HasSuffix(ns.Email(41), "@"+loadtestEmailDomain) {
		t.Fatal("seeded emails must stay under the reserved load-test domain")
	}
	for _, name := range []string{ns.NodeName(0), ns.ServerName(0), ns.PoolCode(), ns.PlanCode()} {
		if !strings.HasPrefix(name, loadtestNodePrefix) {
			t.Fatalf("%q lacks the load-test prefix that retire-previous relies on", name)
		}
	}
	if ns.Email(0) == newNamespace("ci", "000002").Email(0) {
		t.Fatal("two runs must not collide on the unique email constraint")
	}
}

func TestUserIPIsDeterministicInsideBenchmarkNet(t *testing.T) {
	cases := map[int]string{0: "198.18.0.1", 254: "198.18.0.255", 255: "198.18.1.0", 14999: "198.18.58.152", maxUsers - 1: "198.19.255.254"}
	for i, want := range cases {
		got, err := userIP(i)
		if err != nil || got != want {
			t.Fatalf("userIP(%d) = %q, %v; want %q", i, got, err, want)
		}
		if !userNet.Contains(netip.MustParseAddr(got)) {
			t.Fatalf("userIP(%d) = %s escapes %s", i, got, userNet)
		}
	}
	for _, bad := range []int{-1, maxUsers} {
		if _, err := userIP(bad); err == nil {
			t.Fatalf("userIP(%d) should fail", bad)
		}
	}
}

func TestServerIPCyclesThroughTestNet3(t *testing.T) {
	if serverIP(0) != "203.0.113.1" || serverIP(253) != "203.0.113.254" || serverIP(254) != "203.0.113.1" {
		t.Fatalf("serverIP sequence wrong: %s %s %s", serverIP(0), serverIP(253), serverIP(254))
	}
}

func TestRandomValuesHaveExpectedShape(t *testing.T) {
	id, err := newRunID()
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(id) {
		t.Fatalf("newRunID() = %q, %v", id, err)
	}
	pw, err := newUserPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) < 20 || !strings.ContainsAny(pw, "ABCDEFGHJKLMNPQRSTUVWXYZ") ||
		!strings.ContainsAny(pw, "abcdefghijkmnpqrstuvwxyz") || !strings.ContainsAny(pw, "23456789") {
		t.Fatalf("password %q does not mix cases and digits", pw)
	}
	other, _ := newUserPassword()
	if other == pw {
		t.Fatal("passwords must be fresh per run")
	}
}
