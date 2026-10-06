// [INPUT]: 依赖 platform/sourcetest 的 Load、Source 与 TopDecls 读 admin、public、node 三个包的全部非测试源码
// [OUTPUT]: 对外提供 TestHandlersRunNoSQL
// [POS]: api 的跨包守卫（本目录只有测试文件）：后台、门户与节点网关三个包整包不许跑 SQL、不许直接拿连接池查询，读写一律经 domain 服务（第二波 api 卫生收口）；与 response_writes_guard_test.go 并列
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package api

import (
	"go/ast"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// sqlTokens 出现在源码里就说明处理器自己开了事务或拿着行（r.URL.Query() 不含其中任何一个）。
var sqlTokens = []string{"InTx(", "InTxSerializable", "tx.Query", "tx.Exec", "QueryRow(", "pgx.", "pgxpool.", "pgconn."}

// poolQueryMethods 是 db.Pool（内嵌 pgxpool.Pool）上跑查询或拿连接的方法；就绪探针的 Ping 不在其中。
// Query / Exec 只在带参数时算数：r.URL.Query() 不带参数。
var poolQueryMethods = map[string]bool{
	"Query": true, "QueryRow": true, "Exec": true, "Begin": true, "BeginTx": true,
	"Acquire": true, "AcquireFunc": true, "SendBatch": true, "CopyFrom": true,
	"InTx": true, "InTxSerializable": true, "InTxSerializableRetry": true,
}

func TestHandlersRunNoSQL(t *testing.T) {
	for _, dir := range []string{"admin", "public", "node"} {
		pkg := sourcetest.Load(t, dir)
		src := pkg.Source()
		for _, banned := range sqlTokens {
			if strings.Contains(src, banned) {
				t.Errorf("api/%s must not run SQL directly, found %q; move it into the owning domain service", dir, banned)
			}
		}
		checkedImports := map[string]bool{}
		for _, d := range pkg.TopDecls() {
			if !checkedImports[d.File] {
				checkedImports[d.File] = true
				for _, imp := range d.Imports {
					if strings.HasPrefix(imp, "github.com/jackc/pgx/") {
						t.Errorf("api/%s/%s imports %s; handlers reach the database only through domain services", dir, d.File, imp)
					}
				}
			}
			ast.Inspect(d.Node, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !poolQueryMethods[sel.Sel.Name] {
					return true
				}
				if (sel.Sel.Name == "Query" || sel.Sel.Name == "Exec") && len(call.Args) == 0 {
					return true
				}
				t.Errorf("api/%s/%s %s calls %s; handlers reach the database only through domain services",
					dir, d.File, d.Name, sel.Sel.Name)
				return true
			})
		}
	}
}
