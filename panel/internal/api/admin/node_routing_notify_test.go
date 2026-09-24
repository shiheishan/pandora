// [INPUT]: 读取同包 handlers.go 源码中的 nodeSetRouting
// [OUTPUT]: 对外提供 TestNodeSetRoutingNotifiesNodeAfterCommit
// [POS]: api/admin 的缺陷 18 守卫：单节点路由保存要通知节点，且只能在事务提交之后（回滚了的配置不能推出去）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"os"
	"strings"
	"testing"
)

func TestNodeSetRoutingNotifiesNodeAfterCommit(t *testing.T) {
	raw, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	start := strings.Index(src, "func (h *handlers) nodeSetRouting(")
	end := strings.Index(src[start:], "\n}\n")
	if start < 0 || end < 0 {
		t.Fatal("nodeSetRouting not found")
	}
	body := src[start : start+end]
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
