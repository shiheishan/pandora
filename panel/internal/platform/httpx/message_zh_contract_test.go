// [INPUT]: 依赖 go/ast、go/parser 解析 panel/internal 下的全部非测试源码
// [OUTPUT]: 对外提供 TestUserFacingErrorMessagesAreChinese、TestNodeOnlyExemptionsStayOffUserGateways
// [POS]: platform/httpx 的源码契约测试：4xx 文案给页面直接显示（前端已删英文→中文映射，R116），字面量不许是纯英文；节点端给机器看的文案按函数清单豁免
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package httpx

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

//------------------------------------------------------------------------------
// 豁免：只有节点与支付渠道会读到的文案
//------------------------------------------------------------------------------

// nodeOnlyDirs 整个包都只被节点调用（节点网关）。
var nodeOnlyDirs = []string{"api/node"}

// machineOnlyFuncs 与面板共用一个包、但只在节点网关或支付回调里被调用的函数。
// 键是相对 internal 的文件，值是函数名（方法写 接收者.方法）。
// 新增一项之前先确认它不会被 api/admin、api/public 的页面接口调到——
// TestNodeOnlyExemptionsStayOffUserGateways 会反查导出方法。
var machineOnlyFuncs = map[string][]string{
	"domain/nodefabric/enrollment.go": {
		"Service.BeginEnrollment", "Service.CommitEnrollment", "Service.AbortEnrollment",
		"Service.LookupEnrollmentCredential", "validateEnrollmentEvidence",
		"canonicalUUIDField", "decodeCanonicalSHA256",
	},
	"domain/nodefabric/effective_release_service.go": {
		"Service.FetchEffectiveConfig", "Service.applyEffectiveLayersTx",
	},
	"domain/nodefabric/service.go": {
		"Service.Bootstrap", "Service.FetchConfig", "Service.Heartbeat",
		"Service.ReportEffectiveConfigApplied",
	},
	"domain/nodefabric/config_key_transition.go": {"Service.ConfigSigningKeyTransition"},
	// 支付渠道回调：验签失败的回应只有渠道看得到
	"api/public/handlers.go": {"handlers.paymentWebhook"},
}

// allowedEnglish 是无法翻译的专有名词整句（例如只有一个协议名）。目前为空；
// 往里加之前先想想能不能写成「xx 格式不正确」这样的中文句子。
var allowedEnglish = map[string]bool{}

//------------------------------------------------------------------------------
// 扫描
//------------------------------------------------------------------------------

type messageSite struct {
	pos  token.Position
	text string // 表达式里全部字符串字面量拼起来
}

func TestUserFacingErrorMessagesAreChinese(t *testing.T) {
	root := internalRoot(t)
	sites, exempted := scanErrorMessages(t, root)
	// 扫描器坏掉（例如调用形状改了）时会静默扫出 0 条，这里兜底
	if len(sites) < 300 {
		t.Fatalf("only %d error message sites found; scanner no longer matches httpx call shapes", len(sites))
	}
	for file, funcs := range machineOnlyFuncs {
		for _, fn := range funcs {
			if !exempted[file+"#"+fn] {
				t.Errorf("exemption %s %s no longer exists; remove it from machineOnlyFuncs", file, fn)
			}
		}
	}
	for _, s := range sites {
		if pureEnglish(s.text) && !allowedEnglish[s.text] {
			t.Errorf("%s: user-facing error message is English-only: %q (R116: pages show message as-is)", s.pos, s.text)
		}
	}
}

// 豁免清单里的导出方法不能被后台与门户网关调用，否则英文会漏到页面上
func TestNodeOnlyExemptionsStayOffUserGateways(t *testing.T) {
	root := internalRoot(t)
	var methods []string
	for file, funcs := range machineOnlyFuncs {
		if strings.HasPrefix(file, "api/") {
			continue
		}
		for _, fn := range funcs {
			name := fn[strings.LastIndex(fn, ".")+1:]
			if unicode.IsUpper([]rune(name)[0]) {
				methods = append(methods, name)
			}
		}
	}
	for _, dir := range []string{"api/admin", "api/public"} {
		walkGo(t, filepath.Join(root, dir), func(path string, src []byte) {
			for _, m := range methods {
				if strings.Contains(string(src), "."+m+"(") {
					t.Errorf("%s calls node-only %s; its English messages would reach a page", path, m)
				}
			}
		})
	}
}

func internalRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil || filepath.Base(root) != "internal" {
		t.Fatalf("expected to run from internal/platform/httpx, got root %q err=%v", root, err)
	}
	return root
}

func walkGo(t *testing.T, dir string, fn func(path string, src []byte)) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fn(path, src)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// scanErrorMessages 收集四种写法里给页面看的文案：
// httpx.New 的 message、&httpx.Error{Message}、httpx.Invalid 的字段值（字面量 map 与
// 函数内先建 map 再逐键赋值两种）。&httpx.Error 的 Fields 是给程序用的数据
// （current=N 之类），不在此列。非字面量（变量、db.Message）扫不到，靠人工核对。
func scanErrorMessages(t *testing.T, root string) ([]messageSite, map[string]bool) {
	t.Helper()
	var sites []messageSite
	exempted := map[string]bool{}
	fset := token.NewFileSet()
	walkGo(t, root, func(path string, src []byte) {
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, dir := range nodeOnlyDirs {
			if strings.HasPrefix(rel, dir+"/") {
				return
			}
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		inHttpx := f.Name.Name == "httpx"
		skip := map[string]bool{}
		for _, fn := range machineOnlyFuncs[rel] {
			skip[fn] = true
		}
		add := func(e ast.Expr) {
			if text, ok := literalText(e); ok {
				sites = append(sites, messageSite{pos: fset.Position(e.Pos()), text: text})
			}
		}
		for _, decl := range f.Decls {
			body := ast.Node(decl)
			if fd, ok := decl.(*ast.FuncDecl); ok {
				if name := funcName(fd); skip[name] {
					exempted[rel+"#"+name] = true
					continue
				}
				if fd.Body == nil {
					continue
				}
				collectInvalidMaps(fd.Body, add)
				body = fd.Body
			}
			ast.Inspect(body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.CallExpr:
					switch httpxName(x.Fun, inHttpx) {
					case "New":
						if len(x.Args) == 2 {
							add(x.Args[1])
						}
					case "Invalid":
						if len(x.Args) == 1 {
							if cl, ok := x.Args[0].(*ast.CompositeLit); ok {
								for _, el := range cl.Elts {
									if kv, ok := el.(*ast.KeyValueExpr); ok {
										add(kv.Value)
									}
								}
							}
						}
					}
				case *ast.CompositeLit:
					if httpxName(x.Type, inHttpx) == "Error" {
						for _, el := range x.Elts {
							if kv, ok := el.(*ast.KeyValueExpr); ok {
								if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Message" {
									add(kv.Value)
								}
							}
						}
					}
				}
				return true
			})
		}
	})
	return sites, exempted
}

// collectInvalidMaps 找出函数里传给 httpx.Invalid 的 map 变量，收集它的初始化与逐键赋值
func collectInvalidMaps(body *ast.BlockStmt, add func(ast.Expr)) {
	vars := map[string]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && httpxName(c.Fun, false) == "Invalid" && len(c.Args) == 1 {
			if id, ok := c.Args[0].(*ast.Ident); ok {
				vars[id.Name] = true
			}
		}
		return true
	})
	if len(vars) == 0 {
		return
	}
	ast.Inspect(body, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok || len(as.Lhs) != len(as.Rhs) {
			return true
		}
		for i, l := range as.Lhs {
			switch lhs := l.(type) {
			case *ast.IndexExpr:
				if id, ok := lhs.X.(*ast.Ident); ok && vars[id.Name] {
					add(as.Rhs[i])
				}
			case *ast.Ident:
				if cl, ok := as.Rhs[i].(*ast.CompositeLit); ok && vars[lhs.Name] {
					for _, el := range cl.Elts {
						if kv, ok := el.(*ast.KeyValueExpr); ok {
							add(kv.Value)
						}
					}
				}
			}
		}
		return true
	})
}

func httpxName(e ast.Expr, inHttpx bool) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok && id.Name == "httpx" {
			return x.Sel.Name
		}
	case *ast.Ident:
		if inHttpx {
			return x.Name
		}
	}
	return ""
}

func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// literalText 拼出表达式里全部字符串字面量（含拼接与 fmt.Sprintf 的格式串）；一个都没有返回 false
func literalText(e ast.Expr) (string, bool) {
	var b strings.Builder
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if s, err := strconv.Unquote(lit.Value); err == nil {
				b.WriteString(s)
				found = true
			}
		}
		return true
	})
	return b.String(), found
}

// pureEnglish：有拉丁字母、没有一个汉字
func pureEnglish(s string) bool {
	letters := false
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return false
		}
		if r < unicode.MaxASCII && unicode.IsLetter(r) {
			letters = true
		}
	}
	return letters
}
