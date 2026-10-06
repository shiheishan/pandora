// [INPUT]: 依赖 platform/sourcetest 按名取本包与两个兄弟网关（../aegis-public、../aegis-admin）run 的源码
// [OUTPUT]: 对外提供 TestGatewaysConfigureAuditSourceIdentically、TestGatewaysStartPprofFromTheirOwnVariable
// [POS]: cmd/aegis-node 的三网关装配契约（放在最后补齐的这个网关里，另两个网关的测试不重复）：开服之前注入审计来源信息的哈希与加密且写法逐字相同；pprof 各取本域地址、在开服之前起、随停机关闭

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

// 三个网关共用一份 .env，pprof 地址按域分开（config.PprofAddrEnv）。取错域的
// 那个网关会和别人抢端口；忘了 defer Close 停机时诊断端口会悬着。
func TestGatewaysStartPprofFromTheirOwnVariable(t *testing.T) {
	for dir, domain := range map[string]string{
		".":               "DomainNode",
		"../aegis-public": "DomainPublic",
		"../aegis-admin":  "DomainAdmin",
	} {
		run := sourcetest.Load(t, dir).Decl("run")
		start := strings.Index(run, "pprofSrv, err := profiling.Start(cfg.PprofAddrs[config."+domain+"], log)")
		closeAt := strings.Index(run, "defer pprofSrv.Close()")
		serve := strings.Index(run, "server.Run")
		if start < 0 || closeAt < start || serve < closeAt {
			t.Fatalf("%s: pprof must start from config.%s before serving and be closed with defer", dir, domain)
		}
		if strings.Count(run, "profiling.Start(") != 1 {
			t.Fatalf("%s: exactly one pprof listener per gateway", dir)
		}
	}
}
