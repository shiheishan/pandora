// [INPUT]: 依赖 platform/sourcetest 按名取同包 handlers.nodeSetRouting 的源码
// [OUTPUT]: 对外提供 TestNodeSetRoutingNotifiesNodeAfterCommit
// [POS]: api/admin 的缺陷 18 守卫：单节点路由保存要通知节点，且只能在事务提交之后（回滚了的配置不能推出去）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestNodeSetRoutingNotifiesNodeAfterCommit(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("handlers.nodeSetRouting")
	tx := strings.Index(body, "h.d.Pool.InTx(")
	failed := strings.LastIndex(body, "httpx.Fail(w, r, h.d.Log, err)")
	notify := strings.Index(body, "h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)")
	if tx < 0 || notify < 0 {
		t.Fatal("nodeSetRouting must notify the node after saving")
	}
	if notify < failed {
		t.Fatal("NotifyNodeChanged must come after the transaction's error check, i.e. after commit")
	}
}
