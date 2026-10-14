package service

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

func TestFormatPaid(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]decimal.Decimal
		want string
	}{
		{
			name: "nil map",
			in:   nil,
			want: "0",
		},
		{
			name: "空 map",
			in:   map[string]decimal.Decimal{},
			want: "0",
		},
		{
			name: "金额为零",
			in: map[string]decimal.Decimal{
				"CNY": decimal.Zero,
			},
			want: "0",
		},
		{
			name: "多个币种但金额都为零",
			in: map[string]decimal.Decimal{
				"CNY": decimal.Zero,
				"USD": decimal.Zero,
			},
			want: "0",
		},
		{
			name: "单币种有实付",
			in: map[string]decimal.Decimal{
				"CNY": decimal.RequireFromString("12.30"),
			},
			want: "CNY 12.30",
		},
		{
			name: "多币种按字母序排列",
			in: map[string]decimal.Decimal{
				"USD": decimal.RequireFromString("5.00"),
				"CNY": decimal.RequireFromString("12.30"),
			},
			want: "CNY 12.30 / USD 5.00",
		},
		{
			name: "多币种中包含零金额币种",
			in: map[string]decimal.Decimal{
				"CNY": decimal.RequireFromString("12.30"),
				"USD": decimal.Zero,
			},
			want: "CNY 12.30",
		},
		{
			name: "负数防御",
			in: map[string]decimal.Decimal{
				"CNY": decimal.RequireFromString("-1.00"),
			},
			want: "0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatPaid(tc.in); got != tc.want {
				t.Fatalf("formatPaid got %q want %q", got, tc.want)
			}
		})
	}
}

type fakeExportOpsOrders struct {
	stats       map[uuid.UUID]OrderStat
	recent      map[uuid.UUID][]AdminOrderRow
	recentCalls int
	lastLimit   int
}

var _ OpsOrders = (*fakeExportOpsOrders)(nil)

func (f *fakeExportOpsOrders) UserOrderStats(_ context.Context, ids []uuid.UUID) (map[uuid.UUID]OrderStat, error) {
	out := make(map[uuid.UUID]OrderStat)
	for _, id := range ids {
		if st, ok := f.stats[id]; ok {
			out[id] = st
		}
	}
	return out, nil
}

func (f *fakeExportOpsOrders) RecentOrders(_ context.Context, userID uuid.UUID, limit int) ([]AdminOrderRow, error) {
	f.recentCalls++
	f.lastLimit = limit
	rows := f.recent[userID]
	if rows == nil {
		return []AdminOrderRow{}, nil
	}
	return rows, nil
}

func TestExportUsersWithOrders(t *testing.T) {
	e, ctx := newEnv(t)
	actx := adminCtx(t, e, ctx)

	fs := &fakeOpsSubs{current: map[uuid.UUID]CurrentSub{}}
	e.svc.UseOpsSubscriptions(fs)

	tag := bulkTag(t)
	u1 := e.mkUser(t, ctx, uniqEmail(t, tag+"-o1"), "Passw0rdLong1")
	u2 := e.mkUser(t, ctx, uniqEmail(t, tag+"-o2"), "Passw0rdLong1")

	// 1. 端口未装配时回 Unavailable 错误
	_, err := e.svc.ExportUsers(actx, BulkFilter{Query: tag}, 10)
	if codeOf(err) != errs.Unavailable {
		t.Fatalf("未注入订单端口期望 Unavailable，got %v (code %v)", err, codeOf(err))
	}

	// 2. 注入假订单端口后，核对 Orders 与 Paid 填列
	fo := &fakeExportOpsOrders{
		stats: map[uuid.UUID]OrderStat{
			u1.ID: {
				Orders: 3,
				Paid: map[string]decimal.Decimal{
					"USD": decimal.RequireFromString("5.00"),
					"CNY": decimal.RequireFromString("12.30"),
				},
			},
			// u2 没有任何订单
		},
	}
	e.svc.UseOpsOrders(fo)

	rows, err := e.svc.ExportUsers(actx, BulkFilter{Query: tag}, 10)
	if err != nil {
		t.Fatalf("ExportUsers 失败: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("期望 2 行，got %d", len(rows))
	}

	var row1, row2 *ExportRow
	for i := range rows {
		switch rows[i].Email {
		case u1.Email:
			row1 = &rows[i]
		case u2.Email:
			row2 = &rows[i]
		}
	}
	if row1 == nil || row2 == nil {
		t.Fatalf("未找到导出行: row1=%v row2=%v", row1, row2)
	}

	if row1.Orders != 3 {
		t.Fatalf("u1 Orders got %d want 3", row1.Orders)
	}
	if row1.Paid != "CNY 12.30 / USD 5.00" {
		t.Fatalf("u1 Paid got %q want %q", row1.Paid, "CNY 12.30 / USD 5.00")
	}

	if row2.Orders != 0 {
		t.Fatalf("u2 Orders got %d want 0", row2.Orders)
	}
	if row2.Paid != "0" {
		t.Fatalf("u2 Paid got %q want %q", row2.Paid, "0")
	}
}
