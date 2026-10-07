package billing

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

var (
	// 改凭据到期：UPDATE subscription_credentials … SET … expires_at =
	credentialExpiryWrite = regexp.MustCompile(`(?is)\bUPDATE\s+subscription_credentials\b.*?\bSET\b.*?\bexpires_at\s*=`)
	// 改订阅周期末：UPDATE subscriptions … SET（到 WHERE 为止）里有 current_period_end =
	subscriptionUpdateSet = regexp.MustCompile(`(?is)\bUPDATE\s+subscriptions\b.*?\bSET\b(.*?)(?:\bWHERE\b|$)`)
	periodEndAssignment   = regexp.MustCompile(`(?i)\bcurrent_period_end\s*=`)
)

// sqlWriter 是一处含目标 SQL 的顶层函数。
type sqlWriter struct {
	file, fn string
	calls    map[string]bool
}

func (w sqlWriter) key() string { return w.file + ":" + w.fn }

// scanSQLWriters 扫 panel 的 internal、cmd、tools 下全部非测试 Go 源码，
// 返回含有匹配 match 的字符串字面量的顶层函数（连同它直接调用的函数名）。
func scanSQLWriters(t *testing.T, match func(sql string) bool) []sqlWriter {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	var out []sqlWriter
	scanned := 0
	for _, dir := range []string{"internal", "cmd", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
			if err != nil {
				return err
			}
			scanned++
			rel, _ := filepath.Rel(root, path)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				hit := false
				calls := map[string]bool{}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.BasicLit:
						if node.Kind == token.STRING {
							if s, err := strconv.Unquote(node.Value); err == nil && match(s) {
								hit = true
							}
						}
					case *ast.CallExpr:
						switch f := node.Fun.(type) {
						case *ast.Ident:
							calls[f.Name] = true
						case *ast.SelectorExpr:
							calls[f.Sel.Name] = true
						}
					}
					return true
				})
				if hit {
					out = append(out, sqlWriter{file: filepath.ToSlash(rel), fn: fn.Name.Name, calls: calls})
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	// 一个文件都没扫到说明根目录算错了，那这条测试就成了永远通过的空断言
	if scanned < 100 {
		t.Fatalf("only scanned %d Go files under %s; the scan root is wrong", scanned, root)
	}
	return out
}

// TestCredentialExpiryHasSingleWriter 钉住「凭据到期只经 syncCredentialExpiryTx 改」。
//
// 礼品卡延期曾经只改订阅一行，凭据到期停在旧值：过了原到期日订阅拉取 404、门户
// 链接消失（第 2 波规划第 3 条）。续费、变更套餐各自内联一条 UPDATE，没人记得
// 第三条路径也要改。现在全仓只许一处写凭据到期。
func TestCredentialExpiryHasSingleWriter(t *testing.T) {
	writers := scanSQLWriters(t, credentialExpiryWrite.MatchString)
	var got []string
	for _, w := range writers {
		got = append(got, w.key())
	}
	want := []string{"internal/domain/billing/subscription_period.go:syncCredentialExpiryTx"}
	if !slices.Equal(got, want) {
		t.Fatalf("subscription_credentials.expires_at writers = %v, want only %v；"+
			"新的延期路径请调用 syncCredentialExpiryTx（开新周期）或 extendSubscriptionTx（只往后推）", got, want)
	}
}

// TestSubscriptionPeriodWritersSyncCredentials 钉住「改订阅周期末的函数必须同时对齐凭据」。
func TestSubscriptionPeriodWritersSyncCredentials(t *testing.T) {
	writers := scanSQLWriters(t, func(sql string) bool {
		for _, m := range subscriptionUpdateSet.FindAllStringSubmatch(sql, -1) {
			if periodEndAssignment.MatchString(m[1]) {
				return true
			}
		}
		return false
	})
	var got []string
	for _, w := range writers {
		got = append(got, w.key())
		if !w.calls["syncCredentialExpiryTx"] {
			t.Errorf("%s 改了 subscriptions.current_period_end，却没有调用 syncCredentialExpiryTx："+
				"订阅拉取与门户链接按凭据到期判断，周期末与凭据到期会分叉", w.key())
		}
	}
	// 已知的三处：续费、变更套餐（开新周期），以及只往后推的延期。新增一处要在这里登记，
	// 并确认它对齐了本周期配额行（见 extendSubscriptionTx 的注释）。
	want := []string{
		"internal/domain/billing/plan_change.go:fulfillPlanChangeLocked",
		"internal/domain/billing/renewal_fulfill.go:renewSubscriptionTx",
		"internal/domain/billing/subscription_period.go:extendSubscriptionTx",
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("subscriptions.current_period_end writers = %v, want %v", got, want)
	}
}

// TestExtensionPathsShareExtendSubscriptionTx 钉住礼品卡延期与后台加时长都经
// extendSubscriptionTx，不自己写 SQL。
func TestExtensionPathsShareExtendSubscriptionTx(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for _, name := range []string{"GiftGranter.ExtendExpiry", "Service.ExtendSubscriptionAsAdmin"} {
		src := pkg.Decl(name)
		if !strings.Contains(src, "extendSubscriptionTx(ctx, tx, subscriptionExtension{") {
			t.Errorf("%s must extend through extendSubscriptionTx", name)
		}
		if strings.Contains(src, "UPDATE subscriptions") || strings.Contains(src, "UPDATE subscription_credentials") ||
			strings.Contains(src, "UPDATE quota_balances") {
			t.Errorf("%s must not write subscription period SQL itself", name)
		}
	}
	// 本周期配额行只改等于旧周期末的 cycle 行；已用量不动（规则 6：已用量沿用）
	ext := pkg.Decl("extendSubscriptionTx")
	for _, needle := range []string{
		"FOR UPDATE",
		"subscriptionExtendable(change.Status, renewalClosed)",
		"AND period = 'cycle' AND period_end = $4",
		"if change.Rescued {",
		"rescueQuotaTx(ctx, tx, ext, change.PeriodEnd, lapsed, now)",
		"syncCredentialExpiryTx(ctx, tx, ext.TenantID, ext.SubscriptionID, change.PeriodEnd)",
		"'extended'",
	} {
		if !strings.Contains(ext, needle) {
			t.Errorf("extendSubscriptionTx missing %q", needle)
		}
	}
	if strings.Contains(ext, "consumed") {
		t.Error("extendSubscriptionTx must not reset consumed traffic")
	}
	// 救回只给 cycle 行加折算额度（不清已用量）；清零只发生在 day / month 的自然周期对齐
	rescue := pkg.Decl("rescueQuotaTx")
	for _, needle := range []string{
		"SET limit_value = limit_value + $2",
		"AND qb.period IN ('day', 'month')",
	} {
		if !strings.Contains(rescue, needle) {
			t.Errorf("rescueQuotaTx missing %q", needle)
		}
	}
}

func TestValidateAdminExtend(t *testing.T) {
	for _, tc := range []struct {
		days   int
		reason string
		fields []string
	}{
		{30, "补偿线路故障", nil},
		{1, "  五个字原因  ", nil},
		{maxAdminExtendDays, "上限天数测试", nil},
		{0, "补偿线路故障", []string{"days"}},
		{-3, "补偿线路故障", []string{"days"}},
		{maxAdminExtendDays + 1, "补偿线路故障", []string{"days"}},
		{30, "补偿", []string{"reason"}},
		{30, "    ", []string{"reason"}},
		{30, strings.Repeat("长", adminExtendReasonMax+1), []string{"reason"}},
		{0, "", []string{"days", "reason"}},
	} {
		reason, err := validateAdminExtend(tc.days, tc.reason)
		if tc.fields == nil {
			if err != nil || reason != strings.TrimSpace(tc.reason) {
				t.Errorf("validateAdminExtend(%d, %q) = %q, %v", tc.days, tc.reason, reason, err)
			}
			continue
		}
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed {
			t.Errorf("validateAdminExtend(%d, %q) err=%v, want validation_failed", tc.days, tc.reason, err)
			continue
		}
		var keys []string
		for k := range he.Fields {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, tc.fields) {
			t.Errorf("validateAdminExtend(%d, %q) fields=%v want %v", tc.days, tc.reason, keys, tc.fields)
		}
	}
}
