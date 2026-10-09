package routebudget

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 预算的另一半棘轮：PG18 只要求「量出的数与表相等」，挡不住有人把表上的数和量出的数
// 一起改大。Raises 拿合并基点的表与当前的表比，找出变宽的地方；CI（ratchet 命令）
// 见到变宽就要求区间里的提交信息带 Budget-Raise: <理由>，让放宽预算成为一个写明的决定。

// RoutesFile、NodeBudgetFile 是两份预算所在的文件（相对仓库根）。
const (
	RoutesFile     = "panel/tools/routebudget/routes.txt"
	NodeBudgetFile = "panel/internal/api/node/route_budget_pg18_test.go"
)

// RaiseTrailer 是放宽预算时提交信息里要带的一行，必须点名放宽的是哪一条，再写理由：
//
//	Budget-Raise: public GET /v1/me 登录态要多读一次会话标记
//	Budget-Raise: pushWALBudget 留档行多存一列
//
// 只放过被点名的那几条；同一区间里别的放宽照样拦。
const RaiseTrailer = "Budget-Raise:"

// WALBudgetName 是 push 的 WAL 上限在尾注里的名字。
const WALBudgetName = "pushWALBudget"

var (
	pushWALPattern = regexp.MustCompile(`(?m)^const pushWALBudget = (\d+)\s*$`)
	raiseTrailerRe = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(RaiseTrailer) + `[ \t]+(.+?)[ \t]*$`)
)

// Change 是一处变宽：Name 是尾注里点名用的「网关 方法 路由模板」或 pushWALBudget。
type Change struct {
	Name   string
	Detail string
}

func (c Change) String() string { return c.Detail }

// Raises 比较基点与当前的两份预算，返回变宽的地方：
//   - 登记表里某行的库或 Valkey 预算变大；
//   - 填了数的行变回「- -」（退出预算）；
//   - 新出现、且直接带着预算的行（基点没有这一行）：路由改了模板、新行填了更大的数，
//     就是这个样子；基点里「- -」的行第一次填数是正常的首次量测，不算；
//   - push 的 WAL 上限（pushWALBudget）变大。
//
// 基点没有某份文件（预算还没引入）就不比那一份；基点有、当前读不出（挪走、改名）算错误，
// 不能静默放过。
func Raises(baseRoutes, headRoutes, baseNode, headNode []byte) ([]Change, error) {
	var out []Change
	if baseRoutes != nil {
		old, err := parse(baseRoutes)
		if err != nil {
			return nil, fmt.Errorf("base %s: %w", RoutesFile, err)
		}
		if headRoutes == nil {
			return nil, fmt.Errorf("%s not found (moved or renamed? update routebudget.RoutesFile)", RoutesFile)
		}
		cur, err := parse(headRoutes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", RoutesFile, err)
		}
		if len(old) > 0 && len(cur) == 0 {
			return nil, fmt.Errorf("%s has no rows any more", RoutesFile)
		}
		was := map[string]Route{}
		for _, o := range old {
			was[o.Gateway+" "+o.Key()] = o
		}
		now := map[string]Route{}
		for _, r := range cur {
			name := r.Gateway + " " + r.Key()
			now[name] = r
			if _, existed := was[name]; !existed && r.Budgeted {
				out = append(out, Change{name, fmt.Sprintf("%s is a new budgeted row (%d db / %d kv); "+
					"if it replaces a renamed route, compare with the old row", name, r.DB, r.KV)})
			}
		}
		for _, o := range old {
			if !o.Budgeted {
				continue
			}
			name := o.Gateway + " " + o.Key()
			n, ok := now[name]
			switch {
			case !ok:
				// 路由删掉了：登记表测试保证它确实不在路由器里
			case !n.Budgeted:
				out = append(out, Change{name, fmt.Sprintf("%s left the budget (was %d db / %d kv, now - -)",
					name, o.DB, o.KV)})
			case n.DB > o.DB || n.KV > o.KV:
				out = append(out, Change{name, fmt.Sprintf("%s raised from %d db / %d kv to %d db / %d kv",
					name, o.DB, o.KV, n.DB, n.KV)})
			}
		}
	}
	if baseNode != nil {
		if m := pushWALPattern.FindSubmatch(baseNode); m != nil {
			old, _ := strconv.Atoi(string(m[1]))
			n := pushWALPattern.FindSubmatch(headNode)
			if n == nil {
				return nil, fmt.Errorf("%s: const pushWALBudget = <bytes> not found", NodeBudgetFile)
			}
			cur, _ := strconv.Atoi(string(n[1]))
			if cur > old {
				out = append(out, Change{WALBudgetName, fmt.Sprintf("pushWALBudget raised from %d to %d bytes", old, cur)})
			}
		}
	}
	return out, nil
}

// ApprovedNames 读出提交信息里 Budget-Raise 行点名的预算：路由要写全「网关 方法 模板」
// 再跟理由，WAL 写 pushWALBudget 再跟理由；没写理由的不算。
func ApprovedNames(messages string) map[string]bool {
	names := map[string]bool{}
	for _, m := range raiseTrailerRe.FindAllStringSubmatch(messages, -1) {
		f := strings.Fields(m[1])
		switch {
		case len(f) >= 2 && f[0] == WALBudgetName:
			names[WALBudgetName] = true
		case len(f) >= 4:
			names[strings.Join(f[:3], " ")] = true
		}
	}
	return names
}

// Unapproved 返回没被点名放行的变宽。
func Unapproved(changes []Change, messages string) []Change {
	ok := ApprovedNames(messages)
	var out []Change
	for _, c := range changes {
		if !ok[c.Name] {
			out = append(out, c)
		}
	}
	return out
}
