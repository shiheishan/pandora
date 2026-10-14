package tx

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"go.uber.org/zap"
)

func openTestDB(t *testing.T) *db.DB {
	url := os.Getenv("PANDORA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("未设置 PANDORA_TEST_DATABASE_URL")
	}
	d, err := db.Open(context.Background(), db.Options{
		URL:      url,
		MaxConns: 4,
		Logger:   zap.NewNop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.Conn(context.Background()).Exec(`
		CREATE TABLE IF NOT EXISTS tx_test_items (
			id bigserial primary key,
			name text not null
		)`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = d.Conn(context.Background()).Exec(`DROP TABLE IF EXISTS tx_test_items`).Error
	})
	return d
}

func countByName(ctx context.Context, d *db.DB, name string) int64 {
	var n int64
	_ = d.Conn(ctx).Raw(`SELECT COUNT(*) FROM tx_test_items WHERE name = ?`, name).Scan(&n)
	return n
}

func TestDoCommitAndRollback(t *testing.T) {
	d := openTestDB(t)
	m := NewManager(d)
	ctx := context.Background()

	nameOK := "tx_commit_ok"
	if err := m.Do(ctx, func(ctx context.Context) error {
		return d.Conn(ctx).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, nameOK).Error
	}); err != nil {
		t.Fatal(err)
	}
	if countByName(ctx, d, nameOK) != 1 {
		t.Fatal("提交后应能读到行")
	}

	nameFail := "tx_commit_fail"
	err := m.Do(ctx, func(ctx context.Context) error {
		if err := d.Conn(ctx).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, nameFail).Error; err != nil {
			return err
		}
		return errors.New("故意失败")
	})
	if err == nil {
		t.Fatal("期望 fn 错误")
	}
	if countByName(ctx, d, nameFail) != 0 {
		t.Fatal("回滚后不应读到行")
	}
}

func TestNestedDo(t *testing.T) {
	d := openTestDB(t)
	m := NewManager(d)
	ctx := context.Background()
	name := "tx_nested"

	err := m.Do(ctx, func(outer context.Context) error {
		if err := d.Conn(outer).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, name).Error; err != nil {
			return err
		}
		return m.Do(outer, func(inner context.Context) error {
			return errors.New("内层失败")
		})
	})
	if err == nil {
		t.Fatal("期望内层错误向外传播")
	}
	if countByName(ctx, d, name) != 0 {
		t.Fatal("嵌套内层失败应整体回滚")
	}

	if err := m.Do(ctx, func(outer context.Context) error {
		return m.Do(outer, func(inner context.Context) error {
			return d.Conn(inner).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, name+"_ok").Error
		})
	}); err != nil {
		t.Fatal(err)
	}
	if countByName(ctx, d, name+"_ok") != 1 {
		t.Fatal("嵌套成功应只在最外层提交")
	}
}

func TestAfterCommit(t *testing.T) {
	d := openTestDB(t)
	m := NewManager(d)
	ctx := context.Background()

	var runs atomic.Int32
	if err := m.Do(ctx, func(ctx context.Context) error {
		AfterCommit(ctx, "k1", func(context.Context) { runs.Add(1) })
		AfterCommit(ctx, "k1", func(context.Context) { runs.Add(1) })
		return d.Conn(ctx).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, "hook_ok").Error
	}); err != nil {
		t.Fatal(err)
	}
	if runs.Load() != 1 {
		t.Fatalf("同 key 提交后应只执行一次，得到 %d", runs.Load())
	}

	runs.Store(0)
	_ = m.Do(ctx, func(ctx context.Context) error {
		AfterCommit(ctx, "k2", func(context.Context) { runs.Add(1) })
		return errors.New("回滚")
	})
	if runs.Load() != 0 {
		t.Fatal("回滚时不应执行 AfterCommit")
	}

	runs.Store(0)
	AfterCommit(ctx, "no_tx", func(context.Context) { runs.Add(1) })
	if runs.Load() != 1 {
		t.Fatal("无事务时应立即执行 AfterCommit")
	}
}

func TestUncommittedInvisibleOutsideTx(t *testing.T) {
	d := openTestDB(t)
	m := NewManager(d)
	ctx := context.Background()
	name := "tx_isolation"

	err := m.Do(ctx, func(txCtx context.Context) error {
		if err := d.Conn(txCtx).Exec(`INSERT INTO tx_test_items (name) VALUES (?)`, name).Error; err != nil {
			return err
		}
		if countByName(context.Background(), d, name) != 0 {
			return errors.New("提交前不带事务的读取不应看到行")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if countByName(ctx, d, name) != 1 {
		t.Fatal("提交后应可见")
	}
}

// InTx 只认 Do 的回调：外面已有的事务（如 testkit.TxCtx）不算，Do 回调里（新开或加入）都算，Do 返回后又不算。
func TestInTxOnlyInsideDo(t *testing.T) {
	outer := db.WithTxState(context.Background(), db.NewTxState(nil))
	if InTx(outer) {
		t.Fatal("Do 之外不应算在事务里")
	}
	m := &Manager{}
	var inside bool
	if err := m.Do(outer, func(ctx context.Context) error {
		inside = InTx(ctx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("Do 回调里应算在事务里")
	}
	if InTx(outer) {
		t.Fatal("Do 返回后外层 ctx 不应带标记")
	}
}
