// Package routebudget 是三个网关（public、admin、node）的路由登记表，带每条路由的
// 往返预算：一个请求在稳态下发几次数据库语句往返、几次 Valkey 往返。
//
// 登记表在 routes.txt，一行一条路由，是全仓唯一的路由清单：
//   - routes_test.go 遍历三个网关的真实路由器，登记表与路由器必须一一对应
//     （新增、删除、改路径都要同步改表）；以后的模块地图（project-map）直接读它，
//     不再另外生成路由清单；
//   - 填了预算的行由 PG18 测试走真实路由逐条量（门户在 api/public，节点在 api/node），
//     量出的数必须与表上的数相等：多了是超预算，少了说明优化落地了，要把表改小——
//     预算只降不升，是换栈每一步与每一路优化的往返闸门。
//
// 口径（与访问日志的 db_rt / kv_rt 同源，见 platform/roundtrip）：
//   - 库往返只数语句往返（roundtrip.Counts.DB）：一条语句、一个管线批次各算 1，
//     含 BEGIN、COMMIT。语句准备与取连接时的存活探测与连接上的冷热有关，不进预算；
//   - 非受控归还（归还后多一条会话清理）在量过的路由上必须为 0；
//   - Valkey 往返：一条命令或一个管线算 1。
package routebudget

import (
	"bufio"
	"bytes"
	_ "embed"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

//go:embed routes.txt
var routesTxt []byte

// Gateways 是登记表里的网关名，顺序即表内顺序。
var Gateways = []string{"public", "admin", "node"}

// Route 是登记表的一行。
type Route struct {
	Gateway string
	Method  string
	Pattern string
	// Budgeted 为假时 DB、KV 无意义（表上写 -）：还没有 PG18 用例量过。
	Budgeted bool
	DB       int
	KV       int
	// Line 是在 routes.txt 里的行号，报错时用。
	Line int
}

// Key 是访问日志 route 字段的写法：「方法 路由模板」。
func (r Route) Key() string { return r.Method + " " + r.Pattern }

// Load 读出并校验登记表：列数、网关名、预算格式、无重复、按（网关, 模板, 方法）排序。
func Load() ([]Route, error) { return parse(routesTxt) }

func parse(src []byte) ([]Route, error) {
	var out []Route
	seen := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(src))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 5 {
			return nil, fmt.Errorf("routes.txt:%d: want 5 columns (gateway method pattern db kv), got %d", n, len(f))
		}
		r := Route{Gateway: f[0], Method: f[1], Pattern: f[2], Line: n}
		if !slices.Contains(Gateways, r.Gateway) {
			return nil, fmt.Errorf("routes.txt:%d: unknown gateway %q", n, r.Gateway)
		}
		if strings.ToUpper(r.Method) != r.Method || !strings.HasPrefix(r.Pattern, "/") {
			return nil, fmt.Errorf("routes.txt:%d: malformed method or pattern %q %q", n, r.Method, r.Pattern)
		}
		switch {
		case f[3] == "-" && f[4] == "-":
		case f[3] != "-" && f[4] != "-":
			db, err1 := strconv.Atoi(f[3])
			kv, err2 := strconv.Atoi(f[4])
			if err1 != nil || err2 != nil || db < 0 || kv < 0 {
				return nil, fmt.Errorf("routes.txt:%d: budgets must be non-negative integers or both -", n)
			}
			r.Budgeted, r.DB, r.KV = true, db, kv
		default:
			return nil, fmt.Errorf("routes.txt:%d: budget the database and Valkey together, or leave both -", n)
		}
		id := r.Gateway + " " + r.Key()
		if prev, dup := seen[id]; dup {
			return nil, fmt.Errorf("routes.txt:%d: %s already listed at line %d", n, id, prev)
		}
		seen[id] = n
		if len(out) > 0 && compare(out[len(out)-1], r) > 0 {
			return nil, fmt.Errorf("routes.txt:%d: %s is out of order (sort by gateway as listed in Gateways, then pattern, then method)", n, id)
		}
		out = append(out, r)
	}
	return out, sc.Err()
}

// compare 是登记表的排序：网关按 Gateways 的顺序，再按路由模板、方法。
func compare(a, b Route) int {
	if c := slices.Index(Gateways, a.Gateway) - slices.Index(Gateways, b.Gateway); c != 0 {
		return c
	}
	if c := strings.Compare(a.Pattern, b.Pattern); c != 0 {
		return c
	}
	return strings.Compare(a.Method, b.Method)
}

// Format 把一行写成 routes.txt 的样子（报错时给出可直接粘贴的行）。
func Format(r Route) string {
	db, kv := "-", "-"
	if r.Budgeted {
		db, kv = strconv.Itoa(r.DB), strconv.Itoa(r.KV)
	}
	return fmt.Sprintf("%-6s %-7s %-48s %3s %3s", r.Gateway, r.Method, r.Pattern, db, kv)
}
