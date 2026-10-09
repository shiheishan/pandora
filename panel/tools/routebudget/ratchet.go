package routebudget

import (
	"fmt"
	"regexp"
	"strconv"
)

// 预算的另一半棘轮：PG18 只要求「量出的数与表相等」，挡不住有人把表上的数和量出的数
// 一起改大。Raises 拿合并基点的表与当前的表比，找出变宽的地方；CI（ratchet 命令）
// 见到变宽就要求区间里的提交信息带 Budget-Raise: <理由>，让放宽预算成为一个写明的决定。

// RoutesFile、NodeBudgetFile 是两份预算所在的文件（相对仓库根）。
const (
	RoutesFile     = "panel/tools/routebudget/routes.txt"
	NodeBudgetFile = "panel/internal/api/node/route_budget_pg18_test.go"
)

// RaiseTrailer 是放宽预算时提交信息里要带的一行（冒号后写理由）。
const RaiseTrailer = "Budget-Raise:"

var (
	pushWALPattern = regexp.MustCompile(`(?m)^const pushWALBudget = (\d+)\s*$`)
	raiseTrailerRe = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(RaiseTrailer) + `[ \t]*\S`)
)

// Raises 比较基点与当前的两份预算，返回变宽的地方（每条一句）：
//   - 登记表里某行的库或 Valkey 预算变大；
//   - 填了数的行变回「- -」（退出预算）；
//   - push 的 WAL 上限（pushWALBudget）变大。
//
// 基点没有某份文件（预算还没引入）就不比那一份；当前的文件读不出预算算错误，免得改名后静默放过。
func Raises(baseRoutes, headRoutes, baseNode, headNode []byte) ([]string, error) {
	var out []string
	if baseRoutes != nil {
		old, err := parse(baseRoutes)
		if err != nil {
			return nil, fmt.Errorf("base %s: %w", RoutesFile, err)
		}
		cur, err := parse(headRoutes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", RoutesFile, err)
		}
		now := map[string]Route{}
		for _, r := range cur {
			now[r.Gateway+" "+r.Key()] = r
		}
		for _, o := range old {
			if !o.Budgeted {
				continue
			}
			n, ok := now[o.Gateway+" "+o.Key()]
			switch {
			case !ok:
				// 路由删掉了：登记表测试保证它确实不在路由器里
			case !n.Budgeted:
				out = append(out, fmt.Sprintf("%s %s left the budget (was %d db / %d kv, now - -)",
					o.Gateway, o.Key(), o.DB, o.KV))
			case n.DB > o.DB || n.KV > o.KV:
				out = append(out, fmt.Sprintf("%s %s raised from %d db / %d kv to %d db / %d kv",
					o.Gateway, o.Key(), o.DB, o.KV, n.DB, n.KV))
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
				out = append(out, fmt.Sprintf("pushWALBudget raised from %d to %d bytes", old, cur))
			}
		}
	}
	return out, nil
}

// HasRaiseTrailer 报告提交信息里有没有带理由的 Budget-Raise 行。
func HasRaiseTrailer(messages string) bool { return raiseTrailerRe.MatchString(messages) }
