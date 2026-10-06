// [INPUT]: 依赖 payment_query.go 的 queryProvider、payment_query_patrol.go 的 nextPaymentQueryAt 与 DefaultPaymentQueryPatrol，依赖 payment_query_stub_test.go 的渠道替身
// [OUTPUT]: 对外提供 TestNextPaymentQueryAtBacksOffAndKeepsALastCall、TestQueryProviderTranslatesChannelFailures、TestOrderQueryResultNamesEveryOutcome
// [POS]: billing 主动查单的纯逻辑单测：退避与过期前最后一查；渠道停用、不支持、查询失败、缺流水号都在碰数据库之前翻成中文业务错误，订单不动；后台审计的 result 六种取值与 order. 前缀

package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestNextPaymentQueryAtBacksOffAndKeepsALastCall(t *testing.T) {
	p := DefaultPaymentQueryPatrol
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	// 没有过期时间：5、10、20、40 分钟……指数退避
	for attempts, want := range map[int]time.Duration{
		1: 5 * time.Minute, 2: 10 * time.Minute, 3: 20 * time.Minute, 4: 40 * time.Minute,
	} {
		if got := nextPaymentQueryAt(now, attempts, nil, p).Sub(now); got != want {
			t.Errorf("attempts=%d delay=%s want=%s", attempts, got, want)
		}
	}
	// 次数再大也不溢出
	if got := nextPaymentQueryAt(now, 200, nil, p); !got.After(now) {
		t.Fatalf("huge attempts must still schedule in the future, got %s", got)
	}

	// 退避落在过期之后：改到过期前 90 秒最后一查
	expires := now.Add(15 * time.Minute)
	if got, want := nextPaymentQueryAt(now, 3, &expires, p), expires.Add(-90*time.Second); !got.Equal(want) {
		t.Fatalf("last call=%s want=%s", got, want)
	}
	// 退避本来就在最后一查之前：照退避
	if got, want := nextPaymentQueryAt(now, 1, &expires, p), now.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("early attempt=%s want=%s", got, want)
	}
	// 已过了最后一查的点（离现在不足一分钟）：不再提前，照退避排到过期之后，扫描自然不再选中
	soon := now.Add(2 * time.Minute)
	if got := nextPaymentQueryAt(now, 1, &soon, p); !got.After(soon) {
		t.Fatalf("past the last call the next query must land after expiry, got %s", got)
	}
	// 再短的基数也不低于最小间隔：认领后的租约必须长于一次渠道查询
	tiny := p
	tiny.FirstDelay = time.Second
	if got := nextPaymentQueryAt(now, 1, nil, tiny).Sub(now); got < paymentQueryMinGap || paymentQueryMinGap <= channelQueryTimeout {
		t.Fatalf("lease=%s min_gap=%s query_timeout=%s", got, paymentQueryMinGap, channelQueryTimeout)
	}
}

func TestQueryProviderTranslatesChannelFailures(t *testing.T) {
	stub := newQueryStubProvider("stub")
	enabled := true
	svc := &PaymentService{}
	svc.factory = payment.NewFactory(func(context.Context, string, string) (*payment.ProviderRecord, error) {
		return &payment.ProviderRecord{Code: "stub", Adapter: "stub", Enabled: enabled}, nil
	}, time.Nanosecond)
	svc.factory.RegisterAdapter("stub", func(payment.ProviderRecord) (payment.Provider, error) { return stub, nil })

	cases := []struct {
		name           string
		prepare        func()
		code           httpx.Code
		message        string
		isNotSupported bool
	}{
		{"disabled", func() { enabled = false }, httpx.CodeUnavailable, "该支付渠道已停用，无法向渠道查单", false},
		{"not supported", func() { enabled = true; stub.answer("AO-1", nil, payment.ErrNotSupported) },
			httpx.CodeConflict, "该支付渠道不支持主动查单", true},
		{"channel error", func() { stub.answer("AO-1", nil, errors.New("epay: 查询返回 HTTP 502")) },
			httpx.CodeUnavailable, "渠道查单失败，请稍后再试", false},
		{"paid without payment ref", func() {
			stub.answer("AO-1", &payment.QueryResult{Found: true, Status: payment.StatusSucceeded,
				Amount: 1000, Currency: "CNY"}, nil)
		}, httpx.CodeUnavailable, "渠道查单结果缺少支付流水号，无法补记", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.prepare()
			time.Sleep(time.Millisecond) // 让工厂缓存过期，重新读到 enabled
			res, err := svc.queryProvider(context.Background(), "tenant", "order", "AO-1", "stub")
			var he *httpx.Error
			if res != nil || !errors.As(err, &he) || he.Code != tc.code || he.Message != tc.message {
				t.Fatalf("res=%+v err=%v, want %s %q", res, err, tc.code, tc.message)
			}
			if errors.Is(err, payment.ErrNotSupported) != tc.isNotSupported {
				t.Fatalf("errors.Is(ErrNotSupported)=%t, want %t", !tc.isNotSupported, tc.isNotSupported)
			}
		})
	}
}

func TestOrderQueryResultNamesEveryOutcome(t *testing.T) {
	cases := []struct {
		res  *OrderPaymentQuery
		err  error
		want string
	}{
		{&OrderPaymentQuery{ChannelStatus: ChannelPaid, Reconciled: true}, nil, "reconciled"},
		{&OrderPaymentQuery{ChannelStatus: ChannelPaid, AlreadyRecorded: true}, nil, "already_recorded"},
		{&OrderPaymentQuery{ChannelStatus: ChannelPaid}, nil, "paid"},
		{&OrderPaymentQuery{ChannelStatus: ChannelUnpaid}, nil, "unpaid"},
		{&OrderPaymentQuery{ChannelStatus: ChannelNotFound}, nil, "not_found"},
		{nil, httpx.New(httpx.CodeUnavailable, "渠道查单失败，请稍后再试"), "failed"},
	}
	for _, tc := range cases {
		if got := orderQueryResult(tc.res, tc.err); got != tc.want {
			t.Errorf("orderQueryResult(%+v, %v)=%q want %q", tc.res, tc.err, got, tc.want)
		}
	}
	if OrderQueryAuditAction[:6] != "order." {
		t.Fatalf("audit action %q must carry the order. prefix the access log groups by", OrderQueryAuditAction)
	}
}
