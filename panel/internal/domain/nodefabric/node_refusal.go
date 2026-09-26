// [INPUT]: 依赖 platform/db 的 ConstraintName / Message，依赖 platform/httpx 的 Error 与 WithInternal
// [OUTPUT]: 对外提供 NodeStatusRefusal
// [POS]: domain/nodefabric 改节点生命周期时数据库拒绝的统一翻译：后台改状态（api/admin 的 nodeSetStatus）、一步上线（node_activate.go）、一步退役（node_retire.go）三处共用，照 adminops 的 switchRefusal 写法
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"unicode"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// NodeStatusRefusal 把改节点状态时的 check_violation 翻成给后台看的 409。
//
// 状态机触发器 app.guard_node_transition（00005）的 RAISE 不带约束名、文案是中文，
// 原样透传——它说清了从哪到哪不合法。nodes 表的 CHECK 约束报错是英文原句，
// 按约束名给中文原因；认不出的约束写通用中文。英文原句一律只进日志（WithInternal）。
//
// 事实（⑪ 核对）：三处 UPDATE 只写 status、serving_status、desired_config_version、
// row_version 与 entered_status_at。BEFORE 触发器先于 CHECK 执行，非法的 status
// 在触发器就被拒（撞不到 nodes_status_check）；serving_status 来自
// ProjectNodeLifecycle 的固定取值（撞不到 nodes_serving_status_check）；其余 CHECK
// 的列没被改，而 PostgreSQL 校验的是整行、旧值早已合法。所以今天只有触发器能真撞到，
// 下面的约束名翻译是兜底：以后有人往这几条 UPDATE 里加列时不会把英文露给页面。
func NodeStatusRefusal(err error) *httpx.Error {
	var msg string
	switch db.ConstraintName(err) {
	case "":
		if raised := db.Message(err); containsHan(raised) {
			msg = raised
		} else {
			msg = "数据库拒绝了这次节点状态变更"
		}
	case "nodes_status_check":
		msg = "节点状态取值不合法"
	case "nodes_serving_status_check":
		msg = "节点服务状态取值不合法"
	case "nodes_desired_effective_pair_check":
		msg = "节点的目标配置版本不完整，请重新发布节点配置后再改状态"
	case "nodes_applied_effective_pair_check":
		msg = "节点上报的已生效配置不完整，等节点重新上报后再改状态"
	case "nodes_hard_fault_needs_reason":
		msg = "节点标记了硬故障却没有写原因"
	default:
		msg = "节点数据不满足数据库约束，这次状态变更没有生效"
	}
	return httpx.New(httpx.CodeConflict, msg).WithInternal(err)
}

func containsHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}
