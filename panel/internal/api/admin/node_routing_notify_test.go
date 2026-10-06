// [INPUT]: 依赖 platform/sourcetest 按名取同包 handlers.nodeSetRouting 的源码
// [OUTPUT]: 对外提供 TestNodeSetRoutingNotifiesNodeAfterCommit
// [POS]: api/admin 的路由处理器守卫：单节点路由保存要通知节点且只能在服务调用成功之后（回滚了的配置不能推出去，缺陷 18）；admin 整包不跑 SQL 由 api/handler_sql_guard_test.go 守

package admin

import (
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
