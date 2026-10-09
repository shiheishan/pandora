package archguard

import (
	"fmt"
	"go/ast"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// readOnlyTxExemptions 是现有的只读 InTx（「包目录:声明名」→ 这个声明里只读 InTx 的个数），
// 路径相对 panel/，声明名照 sourcetest 的写法（函数名，或「接收者类型.方法名」）。
// 棘轮：改成 QueryRowScoped / QueryScoped / BatchScoped 之后把数减一（减到 0 删掉这行）；
// 数对不上（多了是新增的只读事务，少了是改好了没删）都会变红。
var readOnlyTxExemptions = map[string]int{
	"internal/domain/adminops:Service.DashboardNotificationBacklog": 1,
	"internal/domain/adminops:Service.GetOrder":                     1,
	"internal/domain/adminops:Service.GetOrderPaymentHistory":       1,
	"internal/domain/adminops:Service.GetPlan":                      1,
	"internal/domain/adminops:Service.GetUserGenerationJob":         1,
	"internal/domain/adminops:Service.ListAccessLog":                1,
	"internal/domain/adminops:Service.ListAudit":                    1,
	"internal/domain/adminops:Service.ListIPClusters":               1,
	"internal/domain/adminops:Service.ListPlans":                    1,
	"internal/domain/adminops:Service.ListProviders":                1,
	"internal/domain/adminops:Service.ListRevenueAdjustments":       1,
	"internal/domain/adminops:Service.ListSwitches":                 1,
	"internal/domain/adminops:Service.ListTrafficPacks":             1,
	"internal/domain/adminops:Service.ListUserGenerationJobs":       1,
	"internal/domain/adminops:Service.ListUserGroups":               1,
	"internal/domain/adminops:Service.PlanPools":                    1,
	"internal/domain/adminops:Service.PreviewBulk":                  1,
	"internal/domain/adminops:Service.RevenueTimeseries":            1,
	"internal/domain/adminops:Service.SiteTimezone":                 1,
	"internal/domain/adminops:Service.UserActivity":                 1,
	"internal/domain/adminops:Service.UserFetches":                  1,
	"internal/domain/adminops:Service.UserPeers":                    1,
	"internal/domain/adminops:Service.readDashboardNodeTraffic":     1,
	"internal/domain/adminops:Service.readDashboardUserTraffic":     1,
	"internal/domain/appearance:Service.ListSlots":                  1,
	"internal/domain/appearance:Service.ListThemes":                 1,
	"internal/domain/appearance:Service.Public":                     1,
	"internal/domain/billing:PaymentService.loadProvider":           1,
	"internal/domain/billing:PaymentService.loadQueryTarget":        1,
	"internal/domain/billing:PaymentService.orderIDByProviderRef":   1,
	"internal/domain/billing:PaymentService.orderStatus":            1,
	"internal/domain/billing:Service.AdminCommissionOverview":       1,
	"internal/domain/billing:Service.AdminCouponRedemptions":        1,
	"internal/domain/billing:Service.AdminListCoupons":              1,
	"internal/domain/billing:Service.AdminListWithdrawals":          1,
	"internal/domain/billing:Service.BalanceOf":                     1,
	"internal/domain/billing:Service.CommissionSummary":             1,
	"internal/domain/billing:Service.ListBalanceHistory":            1,
	"internal/domain/billing:Service.ListLatePayments":              1,
	"internal/domain/billing:Service.ListMyCommissionTransfers":     1,
	"internal/domain/billing:Service.ListMyCommissions":             1,
	"internal/domain/billing:Service.ListMyOrders":                  1,
	"internal/domain/billing:Service.ListMyWithdrawals":             1,
	"internal/domain/billing:Service.ListTrafficPacks":              1,
	"internal/domain/billing:Service.ListTrafficResets":             1,
	"internal/domain/billing:Service.ManualOrderOptions":            1,
	"internal/domain/billing:Service.MyOrderDetail":                 1,
	"internal/domain/billing:Service.MyTrafficPacks":                1,
	"internal/domain/billing:Service.PaymentMethods":                1,
	"internal/domain/billing:Service.PortalCatalog":                 1,
	"internal/domain/billing:Service.TrafficResetStats":             1,
	"internal/domain/billing:Service.manualPlacement":               1,
	"internal/domain/certs:Service.ACMESettings":                    1,
	"internal/domain/certs:Service.CertificateDetail":               1,
	"internal/domain/certs:Service.ListCertificates":                1,
	"internal/domain/certs:Service.ListDNSCredentials":              1,
	"internal/domain/certs:Service.loadAccount":                     1,
	"internal/domain/certs:Service.refreshARI":                      1,
	"internal/domain/content:Service.GetAdmin":                      1,
	"internal/domain/content:Service.ListAdmin":                     1,
	"internal/domain/content:Service.visible":                       1,
	"internal/domain/giftcard:Service.ListBatches":                  1,
	"internal/domain/giftcard:Service.ListCodes":                    1,
	"internal/domain/giftcard:Service.ListTemplates":                1,
	"internal/domain/giftcard:Service.ListUsages":                   1,
	"internal/domain/giftcard:Service.MyRedemptions":                1,
	"internal/domain/giftcard:Service.Stats":                        1,
	"internal/domain/identity:Service.ListActiveSessions":           1,
	"internal/domain/identity:Service.ListInvitees":                 1,
	"internal/domain/identity:Service.Login":                        1,
	"internal/domain/nodefabric:Service.GetAdminNode":               1,
	"internal/domain/nodefabric:Service.GetGlobalRouting":           1,
	"internal/domain/nodefabric:Service.GetGroupRouting":            1,
	"internal/domain/nodefabric:Service.GetNodeRouting":             1,
	"internal/domain/nodefabric:Service.GetServer":                  1,
	"internal/domain/nodefabric:Service.ListNodePools":              1,
	"internal/domain/nodefabric:Service.ListRouteGroups":            1,
	"internal/domain/nodefabric:Service.ListServerNodes":            1,
	"internal/domain/nodefabric:Service.ListServers":                1,
	"internal/domain/nodefabric:Service.LoadRouting":                1,
	"internal/domain/nodefabric:Service.LookupEnrollmentCredential": 1,
	"internal/domain/nodefabric:Service.NodeCredentials":            1,
	"internal/domain/nodefabric:Service.PreviewNodeRouting":         1,
	"internal/domain/nodefabric:Service.RefreshTrafficDaily":        1,
	"internal/domain/nodefabric:Service.ServerBinding":              1,
	"internal/domain/nodefabric:Service.activationWarnings":         1,
	"internal/domain/nodefabric:Service.nodeUsers":                  1,
	"internal/domain/notify:LoadSMTPConfig":                         1,
	"internal/domain/notify:LoadTelegramConfig":                     1,
	"internal/domain/notify:Service.BindingInfo":                    1,
	"internal/domain/notify:Service.Inbox":                          1,
	"internal/domain/notify:Service.IssueBindCode":                  1,
	"internal/domain/notify:Service.ListAdminAnnouncements":         1,
	"internal/domain/notify:Service.ListTemplates":                  1,
	"internal/domain/notify:Service.MailSettings":                   1,
	"internal/domain/notify:Service.PreferenceOverrides":            1,
	"internal/domain/notify:Service.TelegramAdminChat":              1,
	"internal/domain/notify:Service.VisibleAnnouncements":           1,
	"internal/domain/notify:Service.deliver":                        1,
	"internal/domain/notify:Service.templateAllowedVariables":       1,
	"internal/domain/notify:loadTelegramToken":                      1,
	"internal/domain/plugin:Service.Deliveries":                     1,
	"internal/domain/plugin:Service.List":                           1,
	"internal/domain/plugin:Service.SaveHook":                       1,
	"internal/domain/plugin:Service.TestHook":                       1,
	"internal/domain/subscription:NodeDeliverability":               1,
	"internal/domain/subscription:Service.DailyUsage":               1,
	"internal/domain/subscription:Service.ListLinks":                1,
	"internal/domain/subscription:Service.ListNodes":                1,
	"internal/domain/subscription:Service.ListOwnedNodePreviews":    1,
	"internal/domain/subscription:Service.LoadPull":                 1,
	"internal/domain/subscription:Service.MySubscriptions":          1,
	"internal/domain/subscription:Service.PathPrefix":               1,
	"internal/domain/subscription:Service.SiteName":                 1,
	"internal/domain/subscription:Service.labelTaken":               1,
	"internal/domain/support:Service.GetForAgent":                   1,
	"internal/domain/support:Service.GetForUser":                    1,
	"internal/domain/support:Service.ListEligibleAssignees":         1,
	"internal/domain/support:Service.ListForAgent":                  1,
	"internal/domain/support:Service.ListForUser":                   1,
	"internal/domain/support:Service.ListMacros":                    1,
	"internal/domain/support:Service.TicketOwner":                   1,
}

// 只读事务的判定（保守：拿不准的一律不算只读，宁可漏报，不误报）：
//   - 闭包（或经同包函数、模块内包级函数传进去的 tx）里出现的字符串里有 SELECT；
//   - 没有任何写或加锁的痕迹（writeMarker）；
//   - tx 没有交给认不出的地方：别的包的方法、存进变量、tx.Begin / CopyFrom 之类。
var (
	selectMarker = regexp.MustCompile(`(?i)\bselect\b`)
	writeMarker  = regexp.MustCompile(`(?i)\b(insert|update|delete|merge|upsert|truncate|create|drop|alter|grant|revoke|lock|copy|notify|listen|vacuum|analyze|refresh|reindex|cluster|comment|call|do)\b|\bfor\s+(no\s+key\s+|key\s+)?(update|share)\b|nextval\s*\(|setval\s*\(|pg_advisory|set_config\s*\(|pg_notify|\bset\s+(local|session|constraints|transaction|role)\b|\bapp\.[a-z_]+\s*\(`)
)

// txReadMethods 是只读闭包里允许对 tx 调用的方法；其余（Begin、CopyFrom、Conn……）算认不出。
var txReadMethods = map[string]bool{"Query": true, "QueryRow": true, "Exec": true, "SendBatch": true}

type pkgIndex struct {
	dir   string // 相对 panel/
	pkg   *sourcetest.Package
	decls map[string][]sourcetest.TopDecl // 声明名（含「类型.方法」与裸方法名）→ 声明
}

type analyzer struct {
	t     *testing.T
	panel string
	pkgs  map[string]*pkgIndex // 相对 panel/ 的包目录
}

func (a *analyzer) load(dir string) *pkgIndex {
	if ix, ok := a.pkgs[dir]; ok {
		return ix
	}
	ix := &pkgIndex{dir: dir, decls: map[string][]sourcetest.TopDecl{}}
	a.pkgs[dir] = ix
	if !hasNonTestGo(filepath.Join(a.panel, dir)) {
		return ix
	}
	ix.pkg = sourcetest.Load(a.t, filepath.Join(a.panel, dir))
	for _, d := range ix.pkg.TopDecls() {
		ix.decls[d.Name] = append(ix.decls[d.Name], d)
		if i := strings.IndexByte(d.Name, '.'); i >= 0 {
			ix.decls[d.Name[i+1:]] = append(ix.decls[d.Name[i+1:]], d)
		}
	}
	return ix
}

func hasNonTestGo(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") && !strings.HasSuffix(e.Name(), "_test.go") {
			return true
		}
	}
	return false
}

// verdict 是一段代码（闭包或它调到的函数）里看到的东西。
type verdict struct {
	sawSelect bool
	write     bool // 有写或加锁的痕迹
	unknown   bool // tx 去了认不出的地方
}

func (v *verdict) addString(s string) {
	if selectMarker.MatchString(s) {
		v.sawSelect = true
	}
	if writeMarker.MatchString(s) {
		v.write = true
	}
}

// scan 分析一段语法树：txName 是 tx 在这段代码里的名字（空串表示只收字符串）。
// seen 防递归。
func (a *analyzer) scan(ix *pkgIndex, imports map[string]string, node ast.Node, txName string, v *verdict, seen map[ast.Node]bool, depth int) {
	if node == nil || seen[node] || depth > 6 {
		if depth > 6 {
			v.unknown = true
		}
		return
	}
	seen[node] = true
	// tx 的合法用法：作为方法接收者调读写方法、作为实参交给能跟进去的函数
	allowed := map[*ast.Ident]bool{}
	ast.Inspect(node, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				if s, err := strconv.Unquote(x.Value); err == nil {
					v.addString(s)
				}
			}
		case *ast.Ident:
			a.resolveIdent(ix, imports, x, v, seen, depth)
		case *ast.SelectorExpr:
			// 别的包的常量 / 变量 / 函数（SQL 片段）：跟进去收字符串
			if pkgID, ok := x.X.(*ast.Ident); ok && pkgID.Obj == nil {
				if path, ok := imports[pkgID.Name]; ok && strings.HasPrefix(path, modulePath) {
					other := a.load(strings.TrimPrefix(path, modulePath))
					for _, d := range other.decls[x.Sel.Name] {
						a.scan(other, d.Imports, d.Node, "", v, seen, depth+1)
					}
				}
			}
		case *ast.CallExpr:
			if txName == "" {
				return true
			}
			// tx.Method(...)
			if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok && id.Name == txName {
					allowed[id] = true
					if !txReadMethods[sel.Sel.Name] {
						v.unknown = true
					}
				}
			}
			for i, arg := range x.Args {
				id, ok := arg.(*ast.Ident)
				if !ok || id.Name != txName {
					continue
				}
				allowed[id] = true
				a.follow(ix, imports, x.Fun, i, v, seen, depth)
			}
		}
		return true
	})
	if txName == "" {
		return
	}
	// tx 的其余出现（赋值、存进结构体、闭包外传）都认不出
	ast.Inspect(node, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == txName && !allowed[id] && !isDeclOf(node, id) {
			v.unknown = true
		}
		return true
	})
}

// isDeclOf 报告 id 是不是 node（函数或闭包）自己的形参声明。
func isDeclOf(node ast.Node, id *ast.Ident) bool {
	var params *ast.FieldList
	switch f := node.(type) {
	case *ast.FuncLit:
		params = f.Type.Params
	case *ast.FuncDecl:
		params = f.Type.Params
	}
	if params == nil {
		return false
	}
	for _, field := range params.List {
		for _, name := range field.Names {
			if name == id {
				return true
			}
		}
	}
	return false
}

// resolveIdent 把标识符指向的字符串收进来：本文件里解析到的局部变量 / 常量看它的初值，
// 解析不到的按同包的顶层声明找。
func (a *analyzer) resolveIdent(ix *pkgIndex, imports map[string]string, id *ast.Ident, v *verdict, seen map[ast.Node]bool, depth int) {
	if id.Obj != nil {
		switch decl := id.Obj.Decl.(type) {
		case *ast.AssignStmt:
			for _, rhs := range decl.Rhs {
				a.scan(ix, imports, rhs, "", v, seen, depth+1)
			}
		case *ast.ValueSpec:
			for _, val := range decl.Values {
				a.scan(ix, imports, val, "", v, seen, depth+1)
			}
		}
		return
	}
	for _, d := range ix.decls[id.Name] {
		if _, isFunc := d.Node.(*ast.FuncDecl); !isFunc {
			a.scan(ix, d.Imports, d.Node, "", v, seen, depth+1)
		}
	}
}

// follow 跟进一个拿到 tx 的调用：同包函数 / 方法（按名字唯一找到）、模块内别的包的
// 包级函数；找不到、找到多个或第 argIndex 个形参取不到名字，都算认不出。
func (a *analyzer) follow(ix *pkgIndex, imports map[string]string, fun ast.Expr, argIndex int, v *verdict, seen map[ast.Node]bool, depth int) {
	var target *pkgIndex
	var name string
	switch f := fun.(type) {
	case *ast.Ident:
		target, name = ix, f.Name
	case *ast.SelectorExpr:
		if pkgID, ok := f.X.(*ast.Ident); ok && pkgID.Obj == nil {
			if path, ok := imports[pkgID.Name]; ok {
				if !strings.HasPrefix(path, modulePath) {
					v.unknown = true
					return
				}
				target, name = a.load(strings.TrimPrefix(path, modulePath)), f.Sel.Name
				break
			}
		}
		target, name = ix, f.Sel.Name // 同包方法：按方法名找
	default:
		v.unknown = true
		return
	}
	var fn *ast.FuncDecl
	var fnImports map[string]string
	for _, d := range target.decls[name] {
		if f, ok := d.Node.(*ast.FuncDecl); ok {
			if fn != nil && fn != f {
				v.unknown = true // 重名（多个类型的同名方法）
				return
			}
			fn, fnImports = f, d.Imports
		}
	}
	if fn == nil || fn.Body == nil {
		v.unknown = true
		return
	}
	param := paramName(fn.Type.Params, argIndex)
	if param == "" {
		v.unknown = true
		return
	}
	a.scan(target, fnImports, fn, param, v, seen, depth+1)
}

func paramName(params *ast.FieldList, index int) string {
	i := 0
	for _, field := range params.List {
		if len(field.Names) == 0 {
			if i == index {
				return ""
			}
			i++
			continue
		}
		for _, n := range field.Names {
			if i == index {
				return n.Name
			}
			i++
		}
	}
	return ""
}

// readOnlyTx 找出一个顶层声明里的只读 InTx，返回它们的行号。
func (a *analyzer) readOnlyTx(ix *pkgIndex, d sourcetest.TopDecl, fset func(token.Pos) int) []int {
	var lines []int
	ast.Inspect(d.Node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 3 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "InTx" {
			return true
		}
		lit, ok := call.Args[len(call.Args)-1].(*ast.FuncLit)
		if !ok {
			return true
		}
		tx := paramName(lit.Type.Params, 0)
		if tx == "" || tx == "_" {
			return true
		}
		var v verdict
		a.scan(ix, d.Imports, lit, tx, &v, map[ast.Node]bool{}, 0)
		if v.sawSelect && !v.write && !v.unknown {
			lines = append(lines, fset(call.Pos()))
		}
		return true
	})
	return lines
}

func TestNoReadOnlyInTx(t *testing.T) {
	a := &analyzer{t: t, panel: filepath.Join("..", ".."), pkgs: map[string]*pkgIndex{}}
	found := map[string][]int{}
	calls := 0
	err := filepath.WalkDir(filepath.Join(a.panel, "internal"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		if d.Name() == "testdata" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(a.panel, path)
		rel = filepath.ToSlash(rel)
		if rel == "internal/platform/db" {
			return nil // InTx 的定义处
		}
		ix := a.load(rel)
		if ix.pkg == nil {
			return nil
		}
		for _, decl := range ix.pkg.TopDecls() {
			if _, ok := decl.Node.(*ast.FuncDecl); !ok {
				continue
			}
			fileAST := decl.Node
			ast.Inspect(fileAST, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "InTx" {
						calls++
					}
				}
				return true
			})
			lines := a.readOnlyTx(ix, decl, func(p token.Pos) int { return int(p) })
			if len(lines) > 0 {
				key := rel + ":" + decl.Name
				found[key] = append(found[key], lines...)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls < 100 {
		t.Fatalf("saw only %d InTx calls; the scanner is broken", calls)
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var table []string
	for _, k := range keys {
		table = append(table, fmt.Sprintf("\t%q: %d,", k, len(found[k])))
		if allowed := readOnlyTxExemptions[k]; len(found[k]) > allowed {
			t.Errorf("%s has %d read-only InTx (exempted %d): an InTx whose closure only SELECTs pays BEGIN and COMMIT "+
				"for nothing; use QueryRowScoped / QueryScoped / BatchScoped (one round trip)", k, len(found[k]), allowed)
		}
	}
	for k, allowed := range readOnlyTxExemptions {
		if n := len(found[k]); n < allowed {
			t.Errorf("%s now has %d read-only InTx, exempted %d: lower the count in readOnlyTxExemptions (delete the line at 0)",
				k, n, allowed)
		}
	}
	if t.Failed() {
		t.Logf("current read-only InTx (%d declarations):\n%s", len(keys), strings.Join(table, "\n"))
	}
}
