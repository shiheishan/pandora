package db_test

// 要库，本机没有 PG 时跳过，以 CI 为准。

import (
	"context"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

func TestStatsCountsInTx(t *testing.T) {
	d := testkit.DB(t)

	s := reqstats.New()
	ctx := reqstats.With(context.Background(), s)

	var n int
	if err := d.Conn(ctx).Raw("SELECT 1").Scan(&n).Error; err != nil {
		t.Fatalf("Raw outside tx: %v", err)
	}
	if got := s.SQLCount(); got != 1 {
		t.Fatalf("事务外 SQLCount = %d, 期望 1", got)
	}
	if s.SQLDuration() <= 0 {
		t.Fatalf("事务外 SQLDuration = %v, 期望 > 0", s.SQLDuration())
	}

	mgr := tx.NewManager(d)
	err := mgr.Do(ctx, func(txCtx context.Context) error {
		if err := d.Conn(txCtx).Raw("SELECT 1").Scan(&n).Error; err != nil {
			return err
		}
		if err := d.Conn(txCtx).Raw("SELECT 1").Scan(&n).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatalf("mgr.Do: %v", err)
	}

	// 事务外 1 次 + 事务内 2 次，共 3 次；BEGIN 与 COMMIT 不计
	if got := s.SQLCount(); got != 3 {
		t.Fatalf("事务后 SQLCount = %d, 期望 3", got)
	}
}
