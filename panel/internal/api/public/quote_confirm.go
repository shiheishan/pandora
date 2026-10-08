package public

import (
	"time"

	"github.com/aegispanel/aegis/internal/domain/billing"
)

// quoteConfirmReq 是四个建单接口共有的两个可选字段（设计稿 2.2）：报价时刻 as_of 与报价的
// 三个数。服务端按同一套函数重算，任一不符回 409 quote_changed；不带 expect 照旧执行。
type quoteConfirmReq struct {
	AsOf   *time.Time        `json:"as_of,omitempty"`
	Expect *quoteExpectation `json:"expect,omitempty"`
}

type quoteExpectation struct {
	Total          int64 `json:"total"`
	BalanceApplied int64 `json:"balance_applied"`
	Payable        int64 `json:"payable"`
}

// expectation 拼成领域输入；没带 expect 时 as_of 也不用（没有可比的东西）。
func (q quoteConfirmReq) expectation() *billing.Expectation {
	if q.Expect == nil {
		return nil
	}
	e := &billing.Expectation{
		Total: q.Expect.Total, BalanceApplied: q.Expect.BalanceApplied, Payable: q.Expect.Payable,
	}
	if q.AsOf != nil {
		e.AsOf = *q.AsOf
	}
	return e
}
