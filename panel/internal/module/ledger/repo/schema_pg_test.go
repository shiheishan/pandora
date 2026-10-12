package repo

// 本文件证明钱的底线确实在库里生效：只追加、提交时配平、单币种、用户类余额不为负、
// 科目归属形状、每单一笔 hold 的唯一索引。直接写 SQL，绕开 Go 层的检查。

import (
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// ledgerTables 是需要真提交的用例开头要清的表。
var ledgerTables = []string{"balance_holds", "ledger_entries", "ledger_transactions", "ledger_accounts"}

// pgCode 取 PostgreSQL 错误码，取不到返回空串。
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// mkUser 插一个用户，返回 ID。
func mkUser(t *testing.T, g *gorm.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	email := fmt.Sprintf("ledger-%s@example.test", uuid.NewString())
	if err := g.Raw("INSERT INTO users (email) VALUES (?) RETURNING id", email).Row().Scan(&id); err != nil {
		t.Fatalf("建用户: %v", err)
	}
	return id
}

// mkPlatformAccount 直接插一个平台类科目。
func mkPlatformAccount(t *testing.T, g *gorm.DB, typ, cur, ref string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := g.Raw(`INSERT INTO ledger_accounts (account_type, currency, owner_ref) VALUES (?, ?, ?) RETURNING id`,
		typ, cur, ref).Row().Scan(&id)
	if err != nil {
		t.Fatalf("建平台科目: %v", err)
	}
	return id
}

// mkUserAccount 直接插一个用户类科目。
func mkUserAccount(t *testing.T, g *gorm.DB, typ, cur string, user uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := g.Raw(`INSERT INTO ledger_accounts (account_type, currency, owner_user_id) VALUES (?, ?, ?) RETURNING id`,
		typ, cur, user).Row().Scan(&id)
	if err != nil {
		t.Fatalf("建用户科目: %v", err)
	}
	return id
}

// insertTxn 直接插一笔交易（操作者 system）。
func insertTxn(g *gorm.DB, kind, cur string, sourceType *string, sourceID *uuid.UUID) (uuid.UUID, error) {
	var id uuid.UUID
	err := g.Raw(`INSERT INTO ledger_transactions (kind, currency, source_type, source_id, actor_kind)
		VALUES (?, ?, ?, ?, 'system') RETURNING id`, kind, cur, sourceType, sourceID).Row().Scan(&id)
	return id, err
}

// insertEntry 直接插一条分录。
func insertEntry(g *gorm.DB, txn, account uuid.UUID, cur, dir, amount string) error {
	return g.Exec(`INSERT INTO ledger_entries (transaction_id, account_id, currency, direction, amount)
		VALUES (?, ?, ?, ?, ?::numeric)`, txn, account, cur, dir, amount).Error
}

// txConn 返回一个测试结束回滚的事务连接。语句出错后这个事务就不能再用，所以预期报错的用例各用一个。
func txConn(t *testing.T, d *db.DB) *gorm.DB {
	t.Helper()
	return d.Conn(testkit.TxCtx(t, d))
}

// postRaw 在 g 上插一笔两条分录的交易：借 debitAmt、贷 creditAmt。
func postRaw(t *testing.T, g *gorm.DB, debitAmt, creditAmt string) (uuid.UUID, error) {
	t.Helper()
	cash := mkPlatformAccount(t, g, "channel_cash", "CNY", "test")
	susp := mkPlatformAccount(t, g, "suspense", "CNY", "manual_adjust")
	txnID, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
	if err != nil {
		return uuid.Nil, err
	}
	if err := insertEntry(g, txnID, cash, "CNY", "debit", debitAmt); err != nil {
		return txnID, err
	}
	return txnID, insertEntry(g, txnID, susp, "CNY", "credit", creditAmt)
}

func TestLedgerBalancedCommits(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, ledgerTables...)
	err := d.Base().Transaction(func(g *gorm.DB) error {
		_, err := postRaw(t, g, "10.00", "10.00")
		return err
	})
	if err != nil {
		t.Fatalf("平账交易应能提交: %v", err)
	}
}

func TestLedgerUnbalancedRejectedAtCommit(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, ledgerTables...)
	var txnID uuid.UUID
	err := d.Base().Transaction(func(g *gorm.DB) error {
		id, err := postRaw(t, g, "10.00", "9.99")
		txnID = id
		// 可延迟约束：语句本身成功，提交时才拦
		if err != nil {
			t.Errorf("插不平的分录时就报错了（应延迟到提交）: %v", err)
		}
		return err
	})
	if pgCode(err) != "23514" {
		t.Fatalf("提交不平的交易: want 23514, got %v", err)
	}
	var n int64
	if err := d.Base().Raw("SELECT count(*) FROM ledger_transactions WHERE id = ?", txnID).Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("提交失败后交易还在: %d", n)
	}
}

func TestLedgerSingleEntryRejectedAtCommit(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, ledgerTables...)
	err := d.Base().Transaction(func(g *gorm.DB) error {
		cash := mkPlatformAccount(t, g, "channel_cash", "CNY", "test")
		txnID, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
		if err != nil {
			return err
		}
		return insertEntry(g, txnID, cash, "CNY", "debit", "10.00")
	})
	if pgCode(err) != "23514" {
		t.Fatalf("只有一条分录: want 23514, got %v", err)
	}
}

func TestLedgerTxnWithoutEntriesRejectedAtCommit(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, ledgerTables...)
	err := d.Base().Transaction(func(g *gorm.DB) error {
		_, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
		return err
	})
	if pgCode(err) != "23514" {
		t.Fatalf("没有分录: want 23514, got %v", err)
	}
}

func TestLedgerImmediateCheck(t *testing.T) {
	d := testkit.DB(t)
	g := txConn(t, d)
	if _, err := postRaw(t, g, "10.00", "9.99"); err != nil {
		t.Fatalf("插分录: %v", err)
	}
	err := g.Exec("SET CONSTRAINTS ALL IMMEDIATE").Error
	if pgCode(err) != "23514" {
		t.Fatalf("SET CONSTRAINTS ALL IMMEDIATE: want 23514, got %v", err)
	}
}

func TestLedgerMixedCurrencyRejected(t *testing.T) {
	d := testkit.DB(t)
	t.Run("交易 CNY 分录 USD", func(t *testing.T) {
		g := txConn(t, d)
		usd := mkPlatformAccount(t, g, "channel_cash", "USD", "test")
		txnID, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertEntry(g, txnID, usd, "USD", "debit", "10.00"); pgCode(err) != "23503" {
			t.Fatalf("want 23503, got %v", err)
		}
	})
	t.Run("分录 CNY 科目 USD", func(t *testing.T) {
		g := txConn(t, d)
		usd := mkPlatformAccount(t, g, "channel_cash", "USD", "test")
		txnID, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := insertEntry(g, txnID, usd, "CNY", "debit", "10.00"); pgCode(err) != "23503" {
			t.Fatalf("want 23503, got %v", err)
		}
	})
}

func TestLedgerAppendOnly(t *testing.T) {
	d := testkit.DB(t)
	testkit.Truncate(t, d, ledgerTables...)
	var txnID uuid.UUID
	if err := d.Base().Transaction(func(g *gorm.DB) error {
		id, err := postRaw(t, g, "10.00", "10.00")
		txnID = id
		return err
	}); err != nil {
		t.Fatalf("提交平账交易: %v", err)
	}
	cases := []struct {
		name string
		sql  string
		args []any
	}{
		{"改交易", `UPDATE ledger_transactions SET memo = 'x' WHERE id = ?`, []any{txnID}},
		{"删交易", `DELETE FROM ledger_transactions WHERE id = ?`, []any{txnID}},
		{"改分录", `UPDATE ledger_entries SET amount = 1 WHERE transaction_id = ?`, []any{txnID}},
		{"删分录", `DELETE FROM ledger_entries WHERE transaction_id = ?`, []any{txnID}},
		{"截断分录", `TRUNCATE ledger_entries`, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := d.Base().Exec(c.sql, c.args...).Error
			if pgCode(err) != "23001" {
				t.Fatalf("want 23001, got %v", err)
			}
		})
	}
	var n int64
	if err := d.Base().Raw("SELECT count(*) FROM ledger_entries WHERE transaction_id = ?", txnID).Row().Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("分录被改动了: 还剩 %d 条", n)
	}
}

func TestLedgerEntryAmountPositive(t *testing.T) {
	d := testkit.DB(t)
	// 不测 1.001：numeric(20,2) 列会先静默舍入，这正是 repo 写入前要 money.Check 的原因
	for _, amount := range []string{"0", "-1"} {
		t.Run(amount, func(t *testing.T) {
			g := txConn(t, d)
			cash := mkPlatformAccount(t, g, "channel_cash", "CNY", "test")
			txnID, err := insertTxn(g, "balance_topup", "CNY", nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := insertEntry(g, txnID, cash, "CNY", "debit", amount); pgCode(err) != "23514" {
				t.Fatalf("want 23514, got %v", err)
			}
		})
	}
}

func TestUserAccountNonNegative(t *testing.T) {
	d := testkit.DB(t)
	t.Run("用户类不能为负", func(t *testing.T) {
		g := txConn(t, d)
		acc := mkUserAccount(t, g, "user_balance", "CNY", mkUser(t, g))
		err := g.Exec("UPDATE ledger_accounts SET balance = -0.01 WHERE id = ?", acc).Error
		if pgCode(err) != "23514" {
			t.Fatalf("want 23514, got %v", err)
		}
	})
	t.Run("平台类可以为负", func(t *testing.T) {
		g := txConn(t, d)
		acc := mkPlatformAccount(t, g, "suspense", "CNY", "manual_adjust")
		if err := g.Exec("UPDATE ledger_accounts SET balance = -5 WHERE id = ?", acc).Error; err != nil {
			t.Fatalf("平台科目应允许负余额: %v", err)
		}
	})
}

func TestAccountOwnerShape(t *testing.T) {
	d := testkit.DB(t)
	cases := []struct {
		name string
		sql  string
		user bool
	}{
		{"用户类不挂用户", `INSERT INTO ledger_accounts (account_type, currency, owner_ref) VALUES ('user_balance', 'CNY', 'x')`, false},
		{"平台类挂了用户", `INSERT INTO ledger_accounts (account_type, currency, owner_user_id, owner_ref) VALUES ('channel_cash', 'CNY', ?, 'x')`, true},
		{"平台类没有 owner_ref", `INSERT INTO ledger_accounts (account_type, currency, owner_ref) VALUES ('channel_cash', 'CNY', '')`, false},
		{"用户类带 owner_ref", `INSERT INTO ledger_accounts (account_type, currency, owner_user_id, owner_ref) VALUES ('user_balance', 'CNY', ?, 'x')`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := txConn(t, d)
			var args []any
			if c.user {
				args = append(args, mkUser(t, g))
			}
			if err := g.Exec(c.sql, args...).Error; pgCode(err) != "23514" {
				t.Fatalf("want 23514, got %v", err)
			}
		})
	}
}

func TestAccountOwnerKeyUnique(t *testing.T) {
	d := testkit.DB(t)
	g := txConn(t, d)
	mkPlatformAccount(t, g, "platform_revenue", "CNY", "main")
	err := g.Exec(`INSERT INTO ledger_accounts (account_type, currency, owner_ref) VALUES ('platform_revenue', 'CNY', 'main')`).Error
	if pgCode(err) != "23505" {
		t.Fatalf("同一平台科目建两次: want 23505, got %v", err)
	}
}

func TestOrderActionUnique(t *testing.T) {
	d := testkit.DB(t)
	src := "order"
	t.Run("同单两笔 hold", func(t *testing.T) {
		g := txConn(t, d)
		order := uuid.New()
		if _, err := insertTxn(g, "balance_hold", "CNY", &src, &order); err != nil {
			t.Fatal(err)
		}
		if _, err := insertTxn(g, "balance_hold", "CNY", &src, &order); pgCode(err) != "23505" {
			t.Fatalf("want 23505, got %v", err)
		}
	})
	t.Run("同单 hold 与 release 各一笔", func(t *testing.T) {
		g := txConn(t, d)
		order := uuid.New()
		if _, err := insertTxn(g, "balance_hold", "CNY", &src, &order); err != nil {
			t.Fatal(err)
		}
		if _, err := insertTxn(g, "balance_release", "CNY", &src, &order); err != nil {
			t.Fatalf("不同 kind 应允许: %v", err)
		}
	})
}

func TestAdjustMemoRequired(t *testing.T) {
	d := testkit.DB(t)
	g := txConn(t, d)
	err := g.Exec(`INSERT INTO ledger_transactions (kind, currency, memo, actor_kind)
		VALUES ('balance_adjusted', 'CNY', '  ab ', 'system')`).Error
	if pgCode(err) != "23514" {
		t.Fatalf("want 23514, got %v", err)
	}
}

func TestBalanceHoldOneHoldPerOrder(t *testing.T) {
	d := testkit.DB(t)
	g := txConn(t, d)
	user := mkUser(t, g)
	order := uuid.New()
	src := "order"
	txnID, err := insertTxn(g, "balance_hold", "CNY", &src, &order)
	if err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO balance_holds (order_id, user_id, currency, amount, hold_txn_id) VALUES (?, ?, 'CNY', 1, ?)`
	if err := g.Exec(ins, order, user, txnID).Error; err != nil {
		t.Fatalf("第一笔冻结: %v", err)
	}
	if err := g.Exec(ins, order, user, txnID).Error; pgCode(err) != "23505" {
		t.Fatalf("同单第二笔冻结: want 23505, got %v", err)
	}
}
