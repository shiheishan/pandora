package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const insertBatchSize = 10000

// col 描述批量插入的一列：name 为列名，typ 为 PG 类型（如 uuid、citext、jsonb）。
type col[T any] struct {
	name string
	typ  string
	val  func(T) *string
}

func buildInsertSQL(table string, names, types []string) string {
	if len(names) != len(types) {
		panic("buildInsertSQL: names/types 长度不一致")
	}
	colList := strings.Join(names, ", ")
	var selectCasts []string
	var unnest []string
	for i, n := range names {
		selectCasts = append(selectCasts, fmt.Sprintf("u.%s::%s", n, types[i]))
		unnest = append(unnest, fmt.Sprintf("$%d::text[]", i+1))
	}
	return fmt.Sprintf(
		"INSERT INTO %s (%s) SELECT %s FROM unnest(%s) AS u(%s)",
		table,
		colList,
		strings.Join(selectCasts, ", "),
		strings.Join(unnest, ", "),
		colList,
	)
}

func insertRows[T any](ctx context.Context, tx pgx.Tx, table string, cols []col[T], rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	names := make([]string, len(cols))
	types := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
		types[i] = c.typ
	}
	sql := buildInsertSQL(table, names, types)
	for start := 0; start < len(rows); start += insertBatchSize {
		end := start + insertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[start:end]
		args := make([]any, len(cols))
		for i, c := range cols {
			vals := make([]*string, len(batch))
			for j, row := range batch {
				vals[j] = c.val(row)
			}
			args[i] = vals
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			return fmt.Errorf("插入表 %s 批次起行 %d: %w", table, start, err)
		}
	}
	return nil
}

func strp(s string) *string { return &s }

func tsp(t time.Time) *string {
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return tsp(*t)
}

func datep(t time.Time) *string {
	s := t.Format("2006-01-02")
	return &s
}

func i64p(n int64) *string {
	s := fmt.Sprintf("%d", n)
	return &s
}

func i64Ptr(p *int64) *string {
	if p == nil {
		return nil
	}
	return i64p(*p)
}

func byteap(b []byte) *string {
	s := "\\x" + hex.EncodeToString(b)
	return &s
}

func optp(p *string) *string { return p }

// zeroNum 是取值恒为 0 的列，按行类型实例化。
func zeroNum[T any](T) *string { return strp("0") }

// writeSeed 按固定顺序把 Seed 写入当前事务。
func writeSeed(ctx context.Context, tx pgx.Tx, s *Seed, offlineProviderID string) error {
	if err := insertRows(ctx, tx, "users", []col[UserRow]{
		{name: "id", typ: "uuid", val: func(r UserRow) *string { return strp(r.ID) }},
		{name: "email", typ: "citext", val: func(r UserRow) *string { return strp(r.Email) }},
		{name: "email_verified_at", typ: "timestamptz", val: func(r UserRow) *string { return tsp(r.EmailVerifiedAt) }},
		{name: "status", typ: "text", val: func(r UserRow) *string { return strp("active") }},
		{name: "user_group_id", typ: "uuid", val: func(r UserRow) *string { return strp(r.GroupID) }},
		{name: "created_at", typ: "timestamptz", val: func(r UserRow) *string { return tsp(r.CreatedAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r UserRow) *string { return tsp(r.CreatedAt) }},
	}, s.Users); err != nil {
		return err
	}
	fmt.Printf("  users %d\n", len(s.Users))

	type pwdRow struct {
		userID string
		at     time.Time
	}
	pr := make([]pwdRow, len(s.Users))
	for i, u := range s.Users {
		pr[i] = pwdRow{u.ID, u.CreatedAt}
	}
	if err := insertRows(ctx, tx, "user_passwords", []col[pwdRow]{
		{name: "user_id", typ: "uuid", val: func(r pwdRow) *string { return strp(r.userID) }},
		{name: "phc", typ: "text", val: func(r pwdRow) *string { return strp(s.PasswordPHC) }},
		{name: "rotated_at", typ: "timestamptz", val: func(r pwdRow) *string { return tsp(r.at) }},
		{name: "created_at", typ: "timestamptz", val: func(r pwdRow) *string { return tsp(r.at) }},
		{name: "updated_at", typ: "timestamptz", val: func(r pwdRow) *string { return tsp(r.at) }},
	}, pr); err != nil {
		return err
	}
	fmt.Printf("  user_passwords %d\n", len(pr))

	if err := insertRows(ctx, tx, "subscriptions", []col[SubscriptionRow]{
		{name: "id", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.ID) }},
		{name: "user_id", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.UserID) }},
		{name: "plan_id", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.PlanID) }},
		{name: "plan_version_id", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.PlanVersionID) }},
		{name: "price_id", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.PriceID) }},
		{name: "status", typ: "text", val: func(r SubscriptionRow) *string { return strp(r.Status) }},
		{name: "current_period_start", typ: "timestamptz", val: func(r SubscriptionRow) *string { return tsp(r.PeriodStart) }},
		{name: "current_period_end", typ: "timestamptz", val: func(r SubscriptionRow) *string { return tsp(r.PeriodEnd) }},
		{name: "snapshot_amount", typ: "numeric", val: func(r SubscriptionRow) *string { return strp(r.Amount) }},
		{name: "snapshot_interval", typ: "text", val: func(r SubscriptionRow) *string { return strp(r.Interval) }},
		{name: "snapshot_interval_count", typ: "smallint", val: func(r SubscriptionRow) *string { return strp(fmt.Sprintf("%d", r.IntervalCount)) }},
		{name: "proxy_uuid", typ: "uuid", val: func(r SubscriptionRow) *string { return strp(r.ProxyUUID) }},
		{name: "created_at", typ: "timestamptz", val: func(r SubscriptionRow) *string { return tsp(r.CreatedAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r SubscriptionRow) *string { return tsp(r.UpdatedAt) }},
	}, s.Subscriptions); err != nil {
		return err
	}
	fmt.Printf("  subscriptions %d\n", len(s.Subscriptions))

	if err := insertRows(ctx, tx, "subscription_credentials", []col[CredentialRow]{
		{name: "id", typ: "uuid", val: func(r CredentialRow) *string { return strp(r.ID) }},
		{name: "subscription_id", typ: "uuid", val: func(r CredentialRow) *string { return strp(r.SubscriptionID) }},
		{name: "user_id", typ: "uuid", val: func(r CredentialRow) *string { return strp(r.UserID) }},
		{name: "token_hash", typ: "bytea", val: func(r CredentialRow) *string { return byteap(r.TokenHash) }},
		{name: "token_prefix", typ: "text", val: func(r CredentialRow) *string { return strp(r.TokenPrefix) }},
		{name: "token_encrypted", typ: "bytea", val: func(r CredentialRow) *string { return byteap(r.TokenEncrypted) }},
		{name: "status", typ: "text", val: func(r CredentialRow) *string { return strp("active") }},
		{name: "created_at", typ: "timestamptz", val: func(r CredentialRow) *string { return tsp(r.CreatedAt) }},
	}, s.Credentials); err != nil {
		return err
	}
	fmt.Printf("  subscription_credentials %d\n", len(s.Credentials))

	if err := insertRows(ctx, tx, "quota_balances", []col[QuotaRow]{
		{name: "id", typ: "uuid", val: func(r QuotaRow) *string { return strp(r.ID) }},
		{name: "subscription_id", typ: "uuid", val: func(r QuotaRow) *string { return strp(r.SubscriptionID) }},
		{name: "metric", typ: "text", val: func(r QuotaRow) *string { return strp(r.Metric) }},
		{name: "period", typ: "text", val: func(r QuotaRow) *string { return strp(r.Period) }},
		{name: "period_start", typ: "timestamptz", val: func(r QuotaRow) *string { return tsp(r.PeriodStart) }},
		{name: "period_end", typ: "timestamptz", val: func(r QuotaRow) *string { return tsPtr(r.PeriodEnd) }},
		{name: "limit_value", typ: "bigint", val: func(r QuotaRow) *string { return i64Ptr(r.Limit) }},
		{name: "consumed", typ: "bigint", val: func(r QuotaRow) *string { return i64p(r.Consumed) }},
		{name: "created_at", typ: "timestamptz", val: func(r QuotaRow) *string { return tsp(r.CreatedAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r QuotaRow) *string { return tsp(r.UpdatedAt) }},
	}, s.Quotas); err != nil {
		return err
	}
	fmt.Printf("  quota_balances %d\n", len(s.Quotas))

	if err := insertRows(ctx, tx, "orders", []col[OrderRow]{
		{name: "id", typ: "uuid", val: func(r OrderRow) *string { return strp(r.ID) }},
		{name: "order_no", typ: "text", val: func(r OrderRow) *string { return strp(r.OrderNo) }},
		{name: "user_id", typ: "uuid", val: func(r OrderRow) *string { return strp(r.UserID) }},
		{name: "kind", typ: "text", val: func(r OrderRow) *string { return strp(r.Kind) }},
		{name: "status", typ: "text", val: func(r OrderRow) *string { return strp("fulfilled") }},
		{name: "subtotal_amount", typ: "numeric", val: func(r OrderRow) *string { return strp(r.Amount) }},
		{name: "total_amount", typ: "numeric", val: func(r OrderRow) *string { return strp(r.Amount) }},
		{name: "payable_amount", typ: "numeric", val: func(r OrderRow) *string { return strp(r.Amount) }},
		{name: "paid_amount", typ: "numeric", val: func(r OrderRow) *string { return strp(r.Amount) }},
		{name: "discount_amount", typ: "numeric", val: zeroNum[OrderRow]},
		{name: "proration_credit_amount", typ: "numeric", val: zeroNum[OrderRow]},
		{name: "waived_amount", typ: "numeric", val: zeroNum[OrderRow]},
		{name: "balance_applied", typ: "numeric", val: zeroNum[OrderRow]},
		{name: "subscription_id", typ: "uuid", val: func(r OrderRow) *string { return strp(r.SubscriptionID) }},
		{name: "state_version", typ: "bigint", val: func(r OrderRow) *string { return strp("3") }},
		{name: "expires_at", typ: "timestamptz", val: func(r OrderRow) *string { return tsp(r.ExpiresAt) }},
		{name: "paid_at", typ: "timestamptz", val: func(r OrderRow) *string { return tsp(r.PaidAt) }},
		{name: "fulfilled_at", typ: "timestamptz", val: func(r OrderRow) *string { return tsp(r.PaidAt) }},
		{name: "created_at", typ: "timestamptz", val: func(r OrderRow) *string { return tsp(r.CreatedAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r OrderRow) *string { return tsp(r.PaidAt) }},
	}, s.Orders); err != nil {
		return err
	}
	fmt.Printf("  orders %d\n", len(s.Orders))

	var newOrderIDs []string
	for _, o := range s.Orders {
		if o.Kind == "new" {
			newOrderIDs = append(newOrderIDs, o.ID)
		}
	}
	ct, err := tx.Exec(ctx, `
		UPDATE subscriptions s SET source_order_id = o.id
		FROM orders o
		WHERE o.subscription_id = s.id AND o.kind = 'new' AND o.id = ANY($1::uuid[])`, newOrderIDs)
	if err != nil {
		return fmt.Errorf("回填 subscriptions.source_order_id: %w", err)
	}
	if int(ct.RowsAffected()) != len(s.Subscriptions) {
		return fmt.Errorf("回填 source_order_id: 期望改 %d 行，实际 %d", len(s.Subscriptions), ct.RowsAffected())
	}
	fmt.Printf("  subscriptions source_order_id 回填 %d\n", len(s.Subscriptions))

	if err := insertRows(ctx, tx, "order_items", []col[OrderItemRow]{
		{name: "id", typ: "uuid", val: func(r OrderItemRow) *string { return strp(r.ID) }},
		{name: "order_id", typ: "uuid", val: func(r OrderItemRow) *string { return strp(r.OrderID) }},
		{name: "price_id", typ: "uuid", val: func(r OrderItemRow) *string { return strp(r.PriceID) }},
		{name: "plan_id", typ: "uuid", val: func(r OrderItemRow) *string { return strp(r.PlanID) }},
		{name: "plan_version_id", typ: "uuid", val: func(r OrderItemRow) *string { return strp(r.PlanVersionID) }},
		{name: "snapshot_name", typ: "text", val: func(r OrderItemRow) *string { return strp(r.Name) }},
		{name: "snapshot_plan_version", typ: "int", val: func(r OrderItemRow) *string { return strp(fmt.Sprintf("%d", r.VersionNo)) }},
		{name: "snapshot_interval", typ: "text", val: func(r OrderItemRow) *string { return strp(r.Interval) }},
		{name: "snapshot_interval_count", typ: "smallint", val: func(r OrderItemRow) *string { return strp(fmt.Sprintf("%d", r.IntervalCount)) }},
		{name: "snapshot_entitlements", typ: "jsonb", val: func(r OrderItemRow) *string { return strp(r.EntitlementsJSON) }},
		{name: "snapshot_quotas", typ: "jsonb", val: func(r OrderItemRow) *string { return strp(r.QuotasJSON) }},
		{name: "quantity", typ: "int", val: func(r OrderItemRow) *string { return strp("1") }},
		{name: "unit_amount", typ: "numeric", val: func(r OrderItemRow) *string { return strp(r.Amount) }},
		{name: "line_amount", typ: "numeric", val: func(r OrderItemRow) *string { return strp(r.Amount) }},
		{name: "created_at", typ: "timestamptz", val: func(r OrderItemRow) *string { return tsp(r.CreatedAt) }},
	}, s.OrderItems); err != nil {
		return err
	}
	fmt.Printf("  order_items %d\n", len(s.OrderItems))

	if err := insertRows(ctx, tx, "order_reservations", []col[ReservationRow]{
		{name: "id", typ: "uuid", val: func(r ReservationRow) *string { return strp(r.ID) }},
		{name: "order_id", typ: "uuid", val: func(r ReservationRow) *string { return strp(r.OrderID) }},
		{name: "user_id", typ: "uuid", val: func(r ReservationRow) *string { return strp(r.UserID) }},
		{name: "state", typ: "text", val: func(r ReservationRow) *string { return strp("captured") }},
		{name: "expires_at", typ: "timestamptz", val: func(r ReservationRow) *string { return tsp(r.ExpiresAt) }},
		{name: "held_at", typ: "timestamptz", val: func(r ReservationRow) *string { return tsp(r.HeldAt) }},
		{name: "captured_at", typ: "timestamptz", val: func(r ReservationRow) *string { return tsp(r.CapturedAt) }},
		{name: "created_at", typ: "timestamptz", val: func(r ReservationRow) *string { return tsp(r.HeldAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r ReservationRow) *string { return tsp(r.CapturedAt) }},
	}, s.Reservations); err != nil {
		return err
	}
	fmt.Printf("  order_reservations %d\n", len(s.Reservations))

	type resvEvtRow struct {
		reservationID string
		orderID       string
		fromState     *string
		toState       string
		eventKind     string
		actorKind     string
		actorID       *string
		occurredAt    time.Time
	}
	var resvEvts []resvEvtRow
	for _, r := range s.Reservations {
		resvEvts = append(resvEvts,
			resvEvtRow{r.ID, r.OrderID, nil, "held", "reserve", "user", strp(r.UserID), r.HeldAt},
			resvEvtRow{r.ID, r.OrderID, strp("held"), "captured", "capture", "payment", nil, r.CapturedAt},
		)
	}
	if err := insertRows(ctx, tx, "order_reservation_events", []col[resvEvtRow]{
		{name: "reservation_id", typ: "uuid", val: func(r resvEvtRow) *string { return strp(r.reservationID) }},
		{name: "order_id", typ: "uuid", val: func(r resvEvtRow) *string { return strp(r.orderID) }},
		{name: "from_state", typ: "text", val: func(r resvEvtRow) *string { return optp(r.fromState) }},
		{name: "to_state", typ: "text", val: func(r resvEvtRow) *string { return strp(r.toState) }},
		{name: "event_kind", typ: "text", val: func(r resvEvtRow) *string { return strp(r.eventKind) }},
		{name: "actor_kind", typ: "text", val: func(r resvEvtRow) *string { return strp(r.actorKind) }},
		{name: "actor_id", typ: "uuid", val: func(r resvEvtRow) *string { return optp(r.actorID) }},
		{name: "occurred_at", typ: "timestamptz", val: func(r resvEvtRow) *string { return tsp(r.occurredAt) }},
	}, resvEvts); err != nil {
		return err
	}
	fmt.Printf("  order_reservation_events %d\n", len(resvEvts))

	if err := insertRows(ctx, tx, "payments", []col[PaymentRow]{
		{name: "id", typ: "uuid", val: func(r PaymentRow) *string { return strp(r.ID) }},
		{name: "order_id", typ: "uuid", val: func(r PaymentRow) *string { return strp(r.OrderID) }},
		{name: "user_id", typ: "uuid", val: func(r PaymentRow) *string { return strp(r.UserID) }},
		{name: "provider_id", typ: "uuid", val: func(r PaymentRow) *string { return strp(offlineProviderID) }},
		{name: "provider_payment_id", typ: "text", val: func(r PaymentRow) *string { return strp(r.ProviderPaymentID) }},
		{name: "amount", typ: "numeric", val: func(r PaymentRow) *string { return strp(r.Amount) }},
		{name: "fee_amount", typ: "numeric", val: zeroNum[PaymentRow]},
		{name: "status", typ: "text", val: func(r PaymentRow) *string { return strp("succeeded") }},
		{name: "method", typ: "text", val: func(r PaymentRow) *string { return strp("offline") }},
		{name: "paid_at", typ: "timestamptz", val: func(r PaymentRow) *string { return tsp(r.PaidAt) }},
		{name: "created_at", typ: "timestamptz", val: func(r PaymentRow) *string { return tsp(r.PaidAt) }},
	}, s.Payments); err != nil {
		return err
	}
	fmt.Printf("  payments %d\n", len(s.Payments))

	if err := insertRows(ctx, tx, "payment_events", []col[PaymentEventRow]{
		{name: "provider_id", typ: "uuid", val: func(r PaymentEventRow) *string { return strp(offlineProviderID) }},
		{name: "provider_event_id", typ: "text", val: func(r PaymentEventRow) *string { return strp(r.ProviderEventID) }},
		{name: "event_type", typ: "text", val: func(r PaymentEventRow) *string { return strp("payment.succeeded") }},
		{name: "provider_payment_id", typ: "text", val: func(r PaymentEventRow) *string { return strp(r.ProviderPaymentID) }},
		{name: "raw_payload", typ: "jsonb", val: func(r PaymentEventRow) *string { return strp(r.RawPayload) }},
		{name: "signature_verified", typ: "boolean", val: func(r PaymentEventRow) *string { return strp("true") }},
		{name: "processing_status", typ: "text", val: func(r PaymentEventRow) *string { return strp("processed") }},
		{name: "processed_at", typ: "timestamptz", val: func(r PaymentEventRow) *string { return tsp(r.ReceivedAt) }},
		{name: "received_at", typ: "timestamptz", val: func(r PaymentEventRow) *string { return tsp(r.ReceivedAt) }},
	}, s.PaymentEvents); err != nil {
		return err
	}
	fmt.Printf("  payment_events %d\n", len(s.PaymentEvents))

	if err := ensureLedgerAccounts(ctx, tx); err != nil {
		return err
	}

	if err := insertRows(ctx, tx, "ledger_transactions", []col[LedgerTxnRow]{
		{name: "id", typ: "uuid", val: func(r LedgerTxnRow) *string { return strp(r.ID) }},
		{name: "kind", typ: "text", val: func(r LedgerTxnRow) *string { return strp("order_paid") }},
		{name: "source_type", typ: "text", val: func(r LedgerTxnRow) *string { return strp("order") }},
		{name: "source_id", typ: "uuid", val: func(r LedgerTxnRow) *string { return strp(r.OrderID) }},
		{name: "memo", typ: "text", val: func(r LedgerTxnRow) *string { return strp("") }},
		{name: "actor_kind", typ: "text", val: func(r LedgerTxnRow) *string { return strp("system") }},
		{name: "occurred_at", typ: "timestamptz", val: func(r LedgerTxnRow) *string { return tsp(r.OccurredAt) }},
	}, s.LedgerTxns); err != nil {
		return err
	}
	fmt.Printf("  ledger_transactions %d\n", len(s.LedgerTxns))

	acct, err := ledgerAccountIDs(ctx, tx)
	if err != nil {
		return err
	}

	type ledgerEntryRow struct {
		transactionID string
		accountID     string
		direction     string
		amount        string
		createdAt     time.Time
	}
	var ledgerEntries []ledgerEntryRow
	for _, t := range s.LedgerTxns {
		ledgerEntries = append(ledgerEntries,
			ledgerEntryRow{t.ID, acct.channelCash, "debit", t.Amount, t.OccurredAt},
			ledgerEntryRow{t.ID, acct.platformRevenue, "credit", t.Amount, t.OccurredAt},
		)
	}
	if err := insertRows(ctx, tx, "ledger_entries", []col[ledgerEntryRow]{
		{name: "transaction_id", typ: "uuid", val: func(r ledgerEntryRow) *string { return strp(r.transactionID) }},
		{name: "account_id", typ: "uuid", val: func(r ledgerEntryRow) *string { return strp(r.accountID) }},
		{name: "direction", typ: "text", val: func(r ledgerEntryRow) *string { return strp(r.direction) }},
		{name: "amount", typ: "numeric", val: func(r ledgerEntryRow) *string { return strp(r.amount) }},
		{name: "created_at", typ: "timestamptz", val: func(r ledgerEntryRow) *string { return tsp(r.createdAt) }},
	}, ledgerEntries); err != nil {
		return err
	}
	fmt.Printf("  ledger_entries %d\n", len(ledgerEntries))

	if _, err := tx.Exec(ctx, `
		UPDATE ledger_accounts a SET balance = a.balance + d.delta, updated_at = now()
		  FROM (SELECT e.account_id,
		               sum(CASE WHEN (la.account_type IN ('channel_cash','platform_fee_expense','platform_promo_expense','suspense')) = (e.direction = 'debit')
		                        THEN e.amount ELSE -e.amount END) AS delta
		          FROM ledger_entries e
		          JOIN ledger_transactions t ON t.id = e.transaction_id
		          JOIN ledger_accounts la ON la.id = e.account_id
		         WHERE t.created_xact = pg_current_xact_id()
		         GROUP BY e.account_id) d
		 WHERE a.id = d.account_id`); err != nil {
		return fmt.Errorf("更新 ledger_accounts 余额: %w", err)
	}
	fmt.Printf("  ledger_accounts 余额已更新\n")

	if err := insertRows(ctx, tx, "subscription_events", []col[SubEventRow]{
		{name: "id", typ: "uuid", val: func(r SubEventRow) *string { return strp(r.ID) }},
		{name: "subscription_id", typ: "uuid", val: func(r SubEventRow) *string { return strp(r.SubscriptionID) }},
		{name: "event_type", typ: "text", val: func(r SubEventRow) *string { return strp(r.EventType) }},
		{name: "from_status", typ: "text", val: func(r SubEventRow) *string { return optp(r.FromStatus) }},
		{name: "to_status", typ: "text", val: func(r SubEventRow) *string { return strp(r.ToStatus) }},
		{name: "actor_kind", typ: "text", val: func(r SubEventRow) *string { return strp(r.ActorKind) }},
		{name: "order_id", typ: "uuid", val: func(r SubEventRow) *string { return optp(r.OrderID) }},
		{name: "payload", typ: "jsonb", val: func(r SubEventRow) *string { return strp(r.Payload) }},
		{name: "occurred_at", typ: "timestamptz", val: func(r SubEventRow) *string { return tsp(r.OccurredAt) }},
	}, s.SubEvents); err != nil {
		return err
	}
	fmt.Printf("  subscription_events %d\n", len(s.SubEvents))

	if err := insertRows(ctx, tx, "traffic_reset_logs", []col[ResetLogRow]{
		{name: "id", typ: "uuid", val: func(r ResetLogRow) *string { return strp(r.ID) }},
		{name: "subscription_id", typ: "uuid", val: func(r ResetLogRow) *string { return strp(r.SubscriptionID) }},
		{name: "user_id", typ: "uuid", val: func(r ResetLogRow) *string { return strp(r.UserID) }},
		{name: "metric", typ: "text", val: func(r ResetLogRow) *string { return strp(r.Metric) }},
		{name: "reason", typ: "text", val: func(r ResetLogRow) *string { return strp(r.Reason) }},
		{name: "consumed_before", typ: "bigint", val: func(r ResetLogRow) *string { return i64p(r.ConsumedBefore) }},
		{name: "consumed_after", typ: "bigint", val: func(r ResetLogRow) *string { return strp("0") }},
		{name: "created_at", typ: "timestamptz", val: func(r ResetLogRow) *string { return tsp(r.CreatedAt) }},
	}, s.ResetLogs); err != nil {
		return err
	}
	fmt.Printf("  traffic_reset_logs %d\n", len(s.ResetLogs))

	if err := insertRows(ctx, tx, "subscription_usage_daily", []col[UsageRow]{
		{name: "id", typ: "uuid", val: func(r UsageRow) *string { return strp(r.ID) }},
		{name: "subscription_id", typ: "uuid", val: func(r UsageRow) *string { return strp(r.SubscriptionID) }},
		{name: "day", typ: "date", val: func(r UsageRow) *string { return datep(r.Day) }},
		{name: "bytes", typ: "bigint", val: func(r UsageRow) *string { return i64p(r.Bytes) }},
		{name: "created_at", typ: "timestamptz", val: func(r UsageRow) *string { return tsp(r.CreatedAt) }},
		{name: "updated_at", typ: "timestamptz", val: func(r UsageRow) *string { return tsp(r.UpdatedAt) }},
	}, s.Usage); err != nil {
		return err
	}
	fmt.Printf("  subscription_usage_daily %d\n", len(s.Usage))

	if err := insertRows(ctx, tx, "audit_events", []col[AuditRow]{
		{name: "actor_kind", typ: "text", val: func(r AuditRow) *string { return strp(r.ActorKind) }},
		{name: "actor_id", typ: "uuid", val: func(r AuditRow) *string { return optp(r.ActorID) }},
		{name: "actor_audience", typ: "text", val: func(r AuditRow) *string { return optp(r.ActorAudience) }},
		{name: "auth_context", typ: "text", val: func(r AuditRow) *string { return optp(r.AuthContext) }},
		{name: "action", typ: "text", val: func(r AuditRow) *string { return strp(r.Action) }},
		{name: "resource_type", typ: "text", val: func(r AuditRow) *string { return strp(r.ResourceType) }},
		{name: "resource_id", typ: "uuid", val: func(r AuditRow) *string { return strp(r.ResourceID) }},
		{name: "outcome", typ: "text", val: func(r AuditRow) *string { return strp("success") }},
		{name: "data", typ: "jsonb", val: func(r AuditRow) *string { return strp(r.Data) }},
		{name: "occurred_at", typ: "timestamptz", val: func(r AuditRow) *string { return tsp(r.OccurredAt) }},
	}, s.Audits); err != nil {
		return err
	}
	fmt.Printf("  audit_events %d\n", len(s.Audits))

	return nil
}

type ledgerAcctIDs struct {
	channelCash     string
	platformRevenue string
}

// ensureLedgerAccounts 建好 offline 渠道现金与平台收入两个科目（已存在就跳过）。
func ensureLedgerAccounts(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO ledger_accounts (account_type, owner_ref) VALUES
		  ('channel_cash', 'offline'),
		  ('platform_revenue', 'main')
		ON CONFLICT ON CONSTRAINT ledger_accounts_owner_key DO NOTHING`); err != nil {
		return fmt.Errorf("确保账本科目: %w", err)
	}
	return nil
}

// ledgerAccountIDs 锁定并返回 offline 渠道现金与平台收入两个科目的 id。
func ledgerAccountIDs(ctx context.Context, tx pgx.Tx) (ledgerAcctIDs, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, account_type FROM ledger_accounts
		 WHERE owner_user_id IS NULL
		   AND ((account_type = 'channel_cash' AND owner_ref = 'offline')
		     OR (account_type = 'platform_revenue' AND owner_ref = 'main'))
		 ORDER BY id FOR UPDATE`)
	if err != nil {
		return ledgerAcctIDs{}, err
	}
	var ids ledgerAcctIDs
	var n int
	for rows.Next() {
		var id, typ string
		if err := rows.Scan(&id, &typ); err != nil {
			rows.Close()
			return ledgerAcctIDs{}, err
		}
		n++
		switch typ {
		case "channel_cash":
			ids.channelCash = id
		case "platform_revenue":
			ids.platformRevenue = id
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return ledgerAcctIDs{}, err
	}
	if n != 2 || ids.channelCash == "" || ids.platformRevenue == "" {
		return ledgerAcctIDs{}, fmt.Errorf("账本科目: 期望锁定 2 行，实际 %d", n)
	}
	return ids, nil
}
