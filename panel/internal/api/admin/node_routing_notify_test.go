// [INPUT]: 依赖 platform/sourcetest 按名取同包 handlers.nodeSetRouting 的源码，依赖 os 读路由处理器源文件
// [OUTPUT]: 对外提供 TestNodeSetRoutingNotifiesNodeAfterCommit、TestRoutingHandlersRunNoSQL
// [POS]: api/admin 的路由处理器守卫：单节点路由保存要通知节点且只能在服务调用成功之后（回滚了的配置不能推出去，缺陷 18）；路由处理器文件不许直接跑 SQL，读写一律在 nodefabric
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

// 路由处理器只做解析、守卫、调用服务与写响应：SQL 与事务在 nodefabric。
func TestRoutingHandlersRunNoSQL(t *testing.T) {
	for _, file := range []string{"node_routing.go", "route_groups.go"} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"InTx(", "tx.Query", "tx.Exec", "QueryRow(", "pgx."} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s must not run SQL directly, found %q", file, banned)
			}
		}
	}
}
