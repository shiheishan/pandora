// [INPUT]: 依赖 platform/sourcetest 按名取本包与两个兄弟网关（../aegis-public、../aegis-admin）run 的源码
// [OUTPUT]: 对外提供 TestGatewaysConfigureAuditSourceIdentically
// [POS]: cmd/aegis-node 的进程装配契约：三个网关都在开服之前注入审计来源信息的哈希与加密，且写法逐字相同
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package main

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 三个网关的审计记录写进同一张 audit_events：哈希函数或加密的附加数据只要有一处
// 不同，按 IP 反查就会把同一来源当成两个；有一处漏注入，那个网关的记录就没有来源。
// 放在 aegis-node 是因为它是最后补上的那个，另两个网关的测试不重复这条。
func TestGatewaysConfigureAuditSourceIdentically(t *testing.T) {
	const (
		hasher = "func(ip string) []byte { return crypto.HashIdentifier(cfg.MasterKey, ip) },"
		sealer = `func(b []byte) ([]byte, error) { return envelope.Seal(b, []byte("audit")) },`
	)
	for _, dir := range []string{".", "../aegis-public", "../aegis-admin"} {
		run := sourcetest.Load(t, dir).Decl("run")
		configure := strings.Index(run, "audit.Configure(")
		serve := strings.Index(run, "server.Run")
		if configure < 0 {
			t.Fatalf("%s: run 没有调用 audit.Configure，审计记录会缺来源信息", dir)
		}
		if serve < 0 || configure > serve {
			t.Fatalf("%s: audit.Configure 必须在开服之前", dir)
		}
		call := run[configure:]
		if end := strings.Index(call, "\n\t)"); end > 0 {
			call = call[:end]
		}
		if !strings.Contains(call, hasher) || !strings.Contains(call, sealer) {
			t.Fatalf("%s: audit.Configure 的哈希或加密与其余网关不一致：\n%s", dir, call)
		}
	}
}
