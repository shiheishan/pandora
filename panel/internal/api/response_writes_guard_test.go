// [INPUT]: 依赖 platform/sourcetest 的 Load 与 TopDecls 逐声明取 admin、public、node 三个包非测试源码的语法树与导入名表
// [OUTPUT]: 对外提供 TestAPIHandlersWriteResponsesOnlyThroughHttpx
// [POS]: api 的跨包守卫（本目录只有测试文件）：处理器不许绕开 platform/httpx 直接写响应，确有理由的按「包目录 + 文件 + 声明名」进白名单，白名单项失效同样变红；与 handler_sql_guard_test.go 并列
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package api

import (
	"go/ast"
	"sort"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

//------------------------------------------------------------------------------
// 白名单：确有理由不走 httpx 的直写
//------------------------------------------------------------------------------

// directWriteAllowed 的键是「包目录/文件 声明名」（方法写 接收者.方法），值是理由。
// 新增一项之前先想想 httpx 的出口（OK / Created / JSON / Fail / NoContent / WritePrepared）
// 能不能表达；不能，再写清楚为什么。
var directWriteAllowed = map[string]string{
	// CSV 导出：带 BOM 的 text/csv 流，不是 JSON
	"admin/audit_log.go handlers.exportAudit":   "审计日志 CSV 导出",
	"admin/bulk_users.go handlers.exportUsers":  "用户 CSV 导出",
	"admin/giftcard.go writeGiftCodesReportCSV": "礼品卡卡密报表 CSV（掩码）",
	"admin/giftcard.go writeGiftBatchCSV":       "礼品卡批次 CSV（生成后一次性的明文卡密下载）",
	// SSE：text/event-stream 长连接逐帧写出并 Flush
	"admin/events.go handlers.events":   "管理端 SSE",
	"public/events.go handlers.events":  "门户 SSE",
	"node/stream.go handlers.uniStream": "节点 SSE",
	// 订阅输出：客户端按 Content-Type 解析 YAML / JSON / base64，失败一律回同一个诱饵 HTML 404
	"public/subscribe.go handlers.subscribe": "订阅正文输出与 429 纯文本",
	"public/subscribe.go writeDecoy":         "订阅失败的诱饵 HTML 404 页",
	// pdnd 安装引导：shell 脚本、二进制与校验和，给 curl 用的纯文本 / 字节流
	"public/pdnd_install.go handlers.pdndInstallScript": "pdnd 安装脚本",
	"public/pdnd_install.go handlers.pdndBinary":        "pdnd 二进制分发（ServeContent 支持断点续传）与纯文本 404",
	"public/pdnd_install.go handlers.pdndChecksum":      "pdnd 校验和（sha256sum 格式纯文本）",
	// 回调回执：格式由对端规定
	"public/handlers.go handlers.paymentWebhook": "支付回调回执，格式与状态由渠道适配器决定",
	"public/telegram.go handlers.telegramUpdate": "Telegram webhook 回执，无论成败固定 200 {\"ok\":true}",
	// 节点配置：nodefabric 预编码的配置字节，ETag 与正文必须是同一份字节；304 协商 httpx 没有出口
	"node/handlers.go handlers.uniConfig": "UniProxy 配置写出预编码字节与 304",
	"node/handlers.go handlers.uniUser":   "UniProxy 用户列表的 ETag 304",
}

// writerMethods 是对 http.ResponseWriter 本身的写调用。
var writerMethods = map[string]bool{"Write": true, "WriteHeader": true, "WriteString": true, "ReadFrom": true}

// writerSinks 是把 ResponseWriter 当作写出目标的标准库函数：键是导入路径，值是函数名。
var writerSinks = map[string]map[string]bool{
	"fmt":           {"Fprint": true, "Fprintf": true, "Fprintln": true},
	"io":            {"Copy": true, "CopyN": true, "CopyBuffer": true, "WriteString": true},
	"encoding/json": {"NewEncoder": true},
	"encoding/csv":  {"NewWriter": true},
	"encoding/xml":  {"NewEncoder": true},
	"bufio":         {"NewWriter": true, "NewWriterSize": true},
	"compress/gzip": {"NewWriter": true, "NewWriterLevel": true},
	"net/http":      {"Error": true, "ServeContent": true, "ServeFile": true, "ServeFileFS": true, "Redirect": true, "NotFound": true},
}

func TestAPIHandlersWriteResponsesOnlyThroughHttpx(t *testing.T) {
	writers := map[string]bool{}
	for _, dir := range []string{"admin", "public", "node"} {
		for _, d := range sourcetest.Load(t, dir).TopDecls() {
			key := dir + "/" + d.File + " " + d.Name
			sites := directWrites(d)
			if len(sites) == 0 {
				continue
			}
			writers[key] = true
			if _, ok := directWriteAllowed[key]; !ok {
				t.Errorf("%s writes the response directly (%s); use platform/httpx or register a reason in directWriteAllowed",
					key, strings.Join(sites, ", "))
			}
		}
	}
	var stale []string
	for key := range directWriteAllowed {
		if !writers[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("directWriteAllowed entry %q no longer exists or no longer writes directly; remove it", key)
	}
}

// directWrites 找出一个顶层声明里对 http.ResponseWriter 的直接写出，返回调用的简写。
// ResponseWriter 认参数：声明本身与其中函数字面量里类型为 http.ResponseWriter 的参数名。
func directWrites(d sourcetest.TopDecl) []string {
	httpName := ""
	for local, path := range d.Imports {
		if path == "net/http" {
			httpName = local
		}
	}
	if httpName == "" {
		return nil
	}
	writers := map[string]bool{}
	ast.Inspect(d.Node, func(n ast.Node) bool {
		ft, ok := n.(*ast.FuncType)
		if !ok || ft.Params == nil {
			return true
		}
		for _, field := range ft.Params.List {
			sel, ok := field.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "ResponseWriter" {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == httpName {
				for _, name := range field.Names {
					writers[name.Name] = true
				}
			}
		}
		return true
	})
	if len(writers) == 0 {
		return nil
	}
	isWriter := func(e ast.Expr) bool {
		id, ok := e.(*ast.Ident)
		return ok && writers[id.Name]
	}
	var sites []string
	ast.Inspect(d.Node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		// w.Write(...) / w.WriteHeader(...)
		if isWriter(sel.X) && writerMethods[sel.Sel.Name] {
			sites = append(sites, sel.Sel.Name)
			return true
		}
		// buf.WriteTo(w)
		if sel.Sel.Name == "WriteTo" && len(call.Args) > 0 && isWriter(call.Args[0]) {
			sites = append(sites, "WriteTo")
			return true
		}
		// fmt.Fprint(w, ...) / io.Copy(w, ...) / json.NewEncoder(w) / http.Error(w, ...)
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || len(call.Args) == 0 || !isWriter(call.Args[0]) {
			return true
		}
		if funcs := writerSinks[d.Imports[pkg.Name]]; funcs[sel.Sel.Name] {
			sites = append(sites, pkg.Name+"."+sel.Sel.Name)
		}
		return true
	})
	return sites
}
