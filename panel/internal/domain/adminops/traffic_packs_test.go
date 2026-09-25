// [INPUT]: 依赖 traffic_packs.go 的 validateTrafficPackInput 与各用例的事务外校验，依赖 catalog_sales_capability_test.go 的 requireCatalogErrorCode
// [OUTPUT]: 对外提供 TestTrafficPackInputValidation、TestTrafficPackWritesFailClosedBeforeTouchingTheDatabase
// [POS]: adminops 流量包目录管理的单元测试：输入边界与「没开销售闸门、没带乐观锁、id 不合法」都在进事务之前被拒
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestTrafficPackInputValidation(t *testing.T) {
	valid := func() TrafficPackInput {
		return TrafficPackInput{Name: "100 GB", TrafficBytes: 100 << 30, Currency: "CNY", UnitAmount: 1500}
	}
	if err := func() error { in := valid(); return validateTrafficPackInput(&in) }(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	cases := map[string]struct {
		mutate func(*TrafficPackInput)
		field  string
	}{
		"blank name":     {func(in *TrafficPackInput) { in.Name = "   " }, "name"},
		"61 char name":   {func(in *TrafficPackInput) { in.Name = strings.Repeat("流", 61) }, "name"},
		"zero bytes":     {func(in *TrafficPackInput) { in.TrafficBytes = 0 }, "traffic_bytes"},
		"over 1 PiB":     {func(in *TrafficPackInput) { in.TrafficBytes = maxTrafficPackBytes + 1 }, "traffic_bytes"},
		"currency":       {func(in *TrafficPackInput) { in.Currency = "EUR" }, "currency"},
		"free pack":      {func(in *TrafficPackInput) { in.UnitAmount = 0 }, "unit_amount"},
		"price overflow": {func(in *TrafficPackInput) { in.UnitAmount = maxTrafficPackAmount + 1 }, "unit_amount"},
		"sort order":     {func(in *TrafficPackInput) { in.SortOrder = 1_000_001 }, "sort_order"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			in := valid()
			tc.mutate(&in)
			var he *httpx.Error
			if err := validateTrafficPackInput(&in); !errors.As(err, &he) ||
				he.Code != httpx.CodeValidationFailed || he.Fields[tc.field] == "" {
				t.Fatalf("err=%v, want 422 on %s", err, tc.field)
			}
		})
	}
	// 60 个汉字是 180 字节，库里的 CHECK 按字数算，这里也按字数放行。
	in := valid()
	in.Name = strings.Repeat("流", 60)
	if err := validateTrafficPackInput(&in); err != nil {
		t.Fatalf("60-rune name rejected: %v", err)
	}
}

// 服务没有数据库（pool 为 nil）：只要走到事务就会 panic，所以这些断言同时
// 证明拒绝发生在进事务之前。
func TestTrafficPackWritesFailClosedBeforeTouchingTheDatabase(t *testing.T) {
	ctx := t.Context()
	const packID = "73300000-0000-7000-8000-000000000099"
	now := time.Now()
	input := TrafficPackInput{ActorID: "actor", ExpectedUpdatedAt: &now,
		Name: "100 GB", TrafficBytes: 100 << 30, Currency: "CNY", UnitAmount: 1500}

	closed := NewService(nil)
	_, err := closed.CreateTrafficPack(ctx, "tenant", input)
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
	_, err = closed.UpdateTrafficPack(ctx, "tenant", packID, input)
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)
	_, err = closed.SetTrafficPackStatus(ctx, "tenant", packID, "actor", "active", &now)
	requireCatalogErrorCode(t, err, httpx.CodeUnavailable)

	open := NewService(nil, staticSalesCapability(true))
	noToken := input
	noToken.ExpectedUpdatedAt = nil
	_, err = open.UpdateTrafficPack(ctx, "tenant", packID, noToken)
	requireCatalogErrorCode(t, err, httpx.CodeValidationFailed)
	_, err = open.SetTrafficPackStatus(ctx, "tenant", packID, "actor", "archived", nil)
	requireCatalogErrorCode(t, err, httpx.CodeValidationFailed)
	_, err = open.SetTrafficPackStatus(ctx, "tenant", packID, "actor", "deleted", &now)
	requireCatalogErrorCode(t, err, httpx.CodeValidationFailed)
	_, err = open.UpdateTrafficPack(ctx, "tenant", "not-a-uuid", input)
	requireCatalogErrorCode(t, err, httpx.CodeNotFound)
	_, err = open.ListTrafficPacks(ctx, "tenant", "draft")
	requireCatalogErrorCode(t, err, httpx.CodeValidationFailed)
}
