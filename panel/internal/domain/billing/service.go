// [INPUT]: 依赖 platform/db 的连接池与 platform/crypto 的信封加密
// [OUTPUT]: 对外提供 Service、NewService、SetUsersChangedNotifier；包内提供 notifyUsersChanged / notifyIfFulfilled 与 addInterval、newOrderNo、jsonAgg、couponID、orderKindFor、nullIfEmpty 等共用小工具
// [POS]: billing 的服务骨架：从 checkout.go 拆出。履约改变交付集合后经注入的 onUsersChanged 在提交后发租户级节点通知，零元单由 notifyIfFulfilled 补发；下单、结算、续费、变更等用例分在同包各文件
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package billing

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

type Service struct {
	pool *db.Pool
	// envelope 用于把订阅 token 可还原地存起来（面板要展示给用户）
	envelope *crypto.Envelope
	// onUsersChanged 在履约事务提交之后调用，告诉节点侧「可服务用户集合变了」。
	//
	// 用回调而不是直接持有 realtime.Hub：那是传输层，计费域不该反向依赖它。
	// 由各网关在装配时注入；为 nil 时行为与接线之前一致。
	onUsersChanged func(ctx context.Context, tenantID string)
}

func NewService(pool *db.Pool, envelope *crypto.Envelope) *Service {
	return &Service{pool: pool, envelope: envelope}
}

// SetUsersChangedNotifier 注入「用户集合已变化」的通知方式。
//
// 不接这个回调时，节点只能靠自己那轮 15 秒轮询发现新用户——付款成功到
// 真正能连上之间会空出十几秒，用户看到的是「付了钱连不上」。
func (s *Service) SetUsersChangedNotifier(fn func(ctx context.Context, tenantID string)) {
	s.onUsersChanged = fn
}

// notifyUsersChanged 只在事务提交之后调用。
//
// 放进事务里发信号，会出现事务回滚了、通知却已经发出去的情况：节点跑去拉
// 一份并不存在的变更，白跑一趟还可能把自己的版本号推歪。
func (s *Service) notifyUsersChanged(ctx context.Context, tenantID string) {
	if s.onUsersChanged == nil {
		return
	}
	s.onUsersChanged(ctx, tenantID)
}

// notifyIfFulfilled 给建单即履约的零元单发通知：赠送、余额或券全额抵扣、零元续费与
// 变更都在建单事务里开通或延长订阅，不经过支付回调，以前节点要等轮询才看到
func (s *Service) notifyIfFulfilled(ctx context.Context, tenantID, status string) {
	if status == "fulfilled" {
		s.notifyUsersChanged(ctx, tenantID)
	}
}

//------------------------------------------------------------------------------
// 辅助
//------------------------------------------------------------------------------

// addInterval 按计费周期推进时间。
//
// 用 AddDate 而非固定天数：AddDate 处理月末与闰年的规则是
// 「1月31日 + 1月 = 3月3日（平年）」，这与多数支付平台一致。
// SUB-010 要求的月末/闰年测试即针对此行为。
func addInterval(from time.Time, interval string, count int) time.Time {
	if count <= 0 {
		count = 1
	}
	switch interval {
	case "day":
		return from.AddDate(0, 0, count)
	case "week":
		return from.AddDate(0, 0, 7*count)
	case "month":
		return from.AddDate(0, count, 0)
	case "quarter":
		return from.AddDate(0, 3*count, 0)
	case "year":
		return from.AddDate(count, 0, 0)
	case "one_time":
		// 一次性商品没有周期，给一个远期哨兵值
		return from.AddDate(100, 0, 0)
	default:
		return from.AddDate(0, count, 0)
	}
}

func newOrderNo() (string, error) {
	suffix, err := crypto.NewToken(6)
	if err != nil {
		return "", err
	}
	return "AO" + time.Now().UTC().Format("20060102") + "-" + suffix[:8], nil
}

func jsonAgg(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]byte, error) {
	var out []byte
	if err := tx.QueryRow(ctx, query, args...).Scan(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// couponID 把可选的券转成可空的参数值。
func couponID(c *couponMatch) any {
	if c == nil {
		return nil
	}
	return c.ID
}

// orderKindFor 决定订单类型。
//
// 人工赠送单也是 'new'，不另起一个 kind。理由：整套预留图与数据库不变量
// 都是围绕 new / renewal / topup 三种形状写的 —— 新增一个 kind 意味着要把
// 库存预留、限购预留、订单项、事件链这些约束逐个教会它，漏一个就是运行时
// 500，而且是那种只在特定路径才暴露的 500。
//
// 而赠送单的形状和普通新购**完全一致**：同一个套餐、同一份订单项快照、
// 同样占库存、同样受限购。区别只在"谁开的"和"为什么开"，
// 这两件事由 created_by 与 manual_reason 记录，本来就独立于 kind。
// 要捞出所有人工单，条件是 created_by IS NOT NULL —— 比 kind='manual'
// 更贴近事实：它说的是"这单是管理员开的"。
func orderKindFor(in CreateOrderInput) string {
	return "new"
}

// nullIfEmpty 让空串落库为 NULL。
// orders 上有 CHECK：kind='manual' 时 manual_reason 必须非空且不短于 5 字。
// 普通订单必须把它留成 NULL 而不是空串，否则约束虽然过得去，
// 数据里却多出一批"理由是空字符串"的行，日后查人工单会把它们一起捞出来。
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
