package admin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 拿了幂等 claim 的 handler 必须回写业务层备好的那一份响应。
//
// 起因是生产上每开一张人工单都报一条 ERROR：
//
//	complete idempotency claim failed / resource_bound
//	idempotency completion CAS rejected
//
// 订单建对了、幂等也生效（重发拿回同一张单），但状态码对不上：
// 首次 200，重发 201。
//
// 根因是同一次请求的响应被写了两份 —— 业务层在事务里
// PrepareJSON(http.StatusCreated, …) 连同状态码写进幂等记录，
// 重发时回放的是它；而 handler 自己又写了个 httpx.OK。两份不一致，
// 幂等中间件事后对账对不上，就报 CAS rejected。
//
// 用户端 createOrder 一直用 WritePrepared（只有一个事实来源），
// 管理端 createManualOrder 落下了。三个包的单元测试全绿也没发现 ——
// 因为没有任何一条测试同时看首次响应和重放响应。
//
// 这条测试锁的就是「只能有一个事实来源」：凡是取了 claim 交给业务层的
// handler，成功路径只能用 httpx.WritePrepared 收尾。
func TestIdempotentHandlersWritePreparedResponse(t *testing.T) {
	// 两个 API 包一起扫。这类不一致哪个包都可能再犯。
	dirs := []string{".", "../public"}

	// 例外：这些 handler 取 claim 只是为了透传或校验，
	// 业务层没有 PrepareJSON，没有可回写的那一份。
	// 加白名单前先确认业务层真的没写幂等响应体。
	allowed := map[string]string{}

	checked := 0
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() || !strings.HasSuffix(name, ".go") ||
				strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				if !idempotencyClaimTaken(fn.Body) {
					continue
				}
				checked++
				if _, skip := allowed[fn.Name.Name]; skip {
					continue
				}
				writers := successResponseWriters(fn.Body)
				if len(writers) == 1 && writers[0] == "httpx.WritePrepared" {
					continue
				}
				t.Errorf("%s 的 %s 取了幂等 claim，成功路径却用 %v 收尾；\n"+
					"业务层已经把响应连同状态码写进幂等记录，这里必须回写那一份"+
					"（httpx.WritePrepared(w, out.PreparedResponse())），"+
					"否则首次响应和重发回放的响应会不一致",
					path, fn.Name.Name, writers)
			}
		}
	}

	// 一个都没扫到，说明识别方式失效了（比如 claim 的取法改了名字），
	// 那这条测试就变成了一条永远通过的空断言 —— 那比没有更糟。
	if checked == 0 {
		t.Fatal("没有扫到任何取幂等 claim 的 handler，检查识别逻辑是否已失效")
	}
}

// idempotencyClaimTaken 判断函数体里是否取过幂等 claim。
func idempotencyClaimTaken(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if idempotentCallName(call.Fun) == "middleware.IdempotencyClaimFrom" {
			found = true
			return false
		}
		return true
	})
	return found
}

// successResponseWriters 收集成功路径上的响应写入方式。
// httpx.Fail 是错误路径，不算。
func successResponseWriters(body *ast.BlockStmt) []string {
	var writers []string
	seen := map[string]bool{}
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := idempotentCallName(call.Fun)
		if !strings.HasPrefix(name, "httpx.") || name == "httpx.Fail" {
			return true
		}
		switch name {
		case "httpx.OK", "httpx.Created", "httpx.WritePrepared", "httpx.NoContent":
			if !seen[name] {
				seen[name] = true
				writers = append(writers, name)
			}
		}
		return true
	})
	return writers
}

func idempotentCallName(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		if base, ok := node.X.(*ast.Ident); ok {
			return base.Name + "." + node.Sel.Name
		}
	}
	return ""
}
