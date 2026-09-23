package billing

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// quarantineUnexpectedPayment records provider-confirmed money that must not
// capture or recreate released resources. The caller already owns the order
// row lock; this function starts with the provider payment evidence lock.
func (s *Service) quarantineUnexpectedPayment(ctx context.Context, tx pgx.Tx,
	tenantID, eventID, providerID, orderID, userID, orderStatus, providerCode,
	caseKind string, in PaymentWebhookInput) (*PaymentWebhookOutput, error) {

	switch caseKind {
	case "released_order", "excess_capture":
		// Supported quarantine classifications.
	default:
		return nil, httpx.New(httpx.CodeBadRequest, "unsupported payment quarantine case")
	}
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(eventID) == "" ||
		strings.TrimSpace(providerID) == "" || strings.TrimSpace(orderID) == "" ||
		strings.TrimSpace(userID) == "" || strings.TrimSpace(orderStatus) == "" ||
		strings.TrimSpace(providerCode) == "" ||
		strings.TrimSpace(in.ProviderPaymentID) == "" {
		return nil, httpx.New(httpx.CodeBadRequest, "payment quarantine identity is incomplete")
	}
	if strings.TrimSpace(in.Currency) == "" || in.Amount <= 0 ||
		in.FeeAmount < 0 || in.FeeAmount > in.Amount {
		return nil, httpx.New(httpx.CodeBadRequest, "payment quarantine amount or currency is invalid")
	}

	// A provider payment can arrive under distinct event IDs concurrently. A
	// transaction-scoped advisory key closes the absent-row gap before the
	// unique payment row exists; the following row lock then owns replays.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		providerID+":"+in.ProviderPaymentID); err != nil {
		return nil, err
	}

	var existingPaymentID, existingOrderID, existingCurrency, existingStatus string
	var existingAmount, existingFee int64
	err := tx.QueryRow(ctx, `
		SELECT id::text,order_id::text,currency::text,amount,fee_amount,status
		  FROM payments
		 WHERE tenant_id=$1 AND provider_id=$2::uuid AND provider_payment_id=$3`,
		tenantID, providerID, in.ProviderPaymentID).
		Scan(&existingPaymentID, &existingOrderID, &existingCurrency,
			&existingAmount, &existingFee, &existingStatus)
	if err == nil {
		if existingOrderID != orderID {
			return nil, httpx.New(httpx.CodeConflict,
				"provider payment is already attached to another order")
		}
		if existingCurrency != in.Currency || existingAmount != in.Amount ||
			existingFee != in.FeeAmount ||
			(existingStatus != "succeeded" && existingStatus != "partially_refunded" &&
				existingStatus != "refunded") {
			return nil, httpx.New(httpx.CodeConflict,
				"provider payment replay does not match recorded money evidence")
		}
		tag, err := tx.Exec(ctx, `
			UPDATE payment_events
			   SET processing_status='ignored',
			       processing_error='provider payment already recorded for order',
			       processed_at=now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND provider_id=$3::uuid
			   AND provider_payment_id=$4 AND processing_status='pending'`,
			tenantID, eventID, providerID, in.ProviderPaymentID)
		if err != nil {
			return nil, err
		}
		if tag.RowsAffected() != 1 {
			return nil, errors.New("duplicate quarantine event transition lost")
		}
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			return nil, err
		}
		return &PaymentWebhookOutput{
			AlreadyHandled: true, PaymentID: existingPaymentID,
		}, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	accountSpecs := []ledgerAccountSpec{
		{
			Key: "channel", AccountType: AccountChannelCash,
			Currency: in.Currency, OwnerRef: providerCode,
		},
		{
			Key: "suspense", AccountType: AccountType("late_payment_suspense"),
			Currency: in.Currency, OwnerRef: "main",
		},
	}
	if in.FeeAmount > 0 {
		accountSpecs = append(accountSpecs, ledgerAccountSpec{
			Key: "fee", AccountType: AccountPlatformFeeExpense,
			Currency: in.Currency, OwnerRef: providerCode,
		})
	}
	accounts, err := prepareAndLockLedgerAccounts(ctx, tx, tenantID, accountSpecs)
	if err != nil {
		return nil, err
	}

	var paymentID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payments
			(tenant_id,order_id,provider_id,provider_payment_id,payment_intent_id,
			 currency,amount,fee_amount,status)
		VALUES ($1,$2::uuid,$3::uuid,$4,NULL,$5,$6,$7,'succeeded')
		RETURNING id::text`, tenantID, orderID, providerID, in.ProviderPaymentID,
		in.Currency, in.Amount, in.FeeAmount).Scan(&paymentID)
	if err != nil {
		return nil, err
	}

	net := in.Amount - in.FeeAmount
	entries := make([]Entry, 0, 3)
	if net > 0 {
		entries = append(entries, Entry{
			AccountID: accounts["channel"], Direction: Debit, Amount: net,
			Description: "unexpected payment channel net receipt",
		})
	}
	if in.FeeAmount > 0 {
		entries = append(entries, Entry{
			AccountID: accounts["fee"], Direction: Debit, Amount: in.FeeAmount,
			Description: "unexpected payment provider fee",
		})
	}
	entries = append(entries, Entry{
		AccountID: accounts["suspense"], Direction: Credit, Amount: in.Amount,
		Description: "unexpected payment held in suspense",
	})
	suspenseTxnID, err := Post(ctx, tx, tenantID, Posting{
		Kind: "late_payment_suspense", Currency: in.Currency,
		SourceType: "payment", SourceID: &paymentID,
		Memo: "quarantine unexpected payment: " + caseKind, ActorKind: "system",
		Entries: entries,
	})
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		INSERT INTO late_payment_cases
			(tenant_id,order_id,payment_id,payment_event_id,case_kind,
			 amount,currency,suspense_txn_id)
		VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid)`,
		tenantID, orderID, paymentID, eventID, caseKind,
		in.Amount, in.Currency, suspenseTxnID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, errors.New("late payment quarantine case insert lost")
	}

	tag, err = tx.Exec(ctx, `
		UPDATE payment_events
		   SET processing_status='processed',processing_error=NULL,processed_at=now()
		 WHERE tenant_id=$1 AND id=$2::uuid AND provider_id=$3::uuid
		   AND provider_payment_id=$4 AND processing_status='pending'`,
		tenantID, eventID, providerID, in.ProviderPaymentID)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, errors.New("quarantined payment event transition lost")
	}

	if err := audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "system", Action: "payment.quarantined",
		ResourceType: "payment", ResourceID: &paymentID,
		AfterDigest: map[string]any{
			"case_kind": caseKind, "order_id": orderID, "order_status": orderStatus,
			"user_id": userID, "provider_payment_id": in.ProviderPaymentID,
			"amount": in.Amount, "fee_amount": in.FeeAmount,
			"currency": in.Currency, "suspense_txn": suspenseTxnID,
		},
		APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
	}); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return nil, err
	}

	return &PaymentWebhookOutput{
		Processed: true, PaymentID: paymentID, LedgerTxnID: suspenseTxnID,
	}, nil
}
