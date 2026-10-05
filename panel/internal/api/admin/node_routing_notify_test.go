// [INPUT]: 依赖 platform/sourcetest 按名取同包 handlers.nodeSetRouting 的源码，依赖 os 读 netOpsHandlerFiles 列出的处理器源文件
// [OUTPUT]: 对外提供 TestNodeSetRoutingNotifiesNodeAfterCommit、TestNetOpsHandlersRunNoSQL
// [POS]: api/admin 的路由处理器守卫：单节点路由保存要通知节点且只能在服务调用成功之后（回滚了的配置不能推出去，缺陷 18）；网络 / 运维 / 安全这 15 个处理器文件（netOpsHandlerFiles）不许直接跑 SQL，读写一律在 nodefabric / adminops
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestNodeSetRoutingNotifiesNodeAfterCommit(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("handlers.nodeSetRouting")
	save := strings.Index(body, "h.d.Node.SetNodeRouting(")
	failed := strings.LastIndex(body, "httpx.Fail(w, r, h.d.Log, err)")
	notify := strings.Index(body, "h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)")
	if save < 0 || notify < 0 {
		t.Fatal("nodeSetRouting must save through nodefabric and notify the node afterwards")
	}
	if notify < failed || notify < save {
		t.Fatal("NotifyNodeChanged must come after the save's error check, i.e. after commit")
	}
}

// netOpsHandlerFiles 是 admin 里网络、运维、安全这一半的处理器文件：只做解析、守卫、
// 调用服务与写响应，SQL 与事务在 nodefabric / adminops。商业、内容那一半由另一份守卫管。
var netOpsHandlerFiles = []string{
	"nodes.go", "pools.go", "pool_user_groups.go", "devices.go", "node_admin.go",
	"node_routing.go", "route_groups.go", "server.go", "system_components.go", "system_status.go",
	"profile.go", "access_log.go", "audit_log.go", "risk.go", "events.go",
}

func TestNetOpsHandlersRunNoSQL(t *testing.T) {
	for _, file := range netOpsHandlerFiles {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"InTx(", "tx.Query", "tx.Exec", "QueryRow(", "pgx.", "d.Pool."} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s must not run SQL directly, found %q", file, banned)
			}
		}
	}
}
