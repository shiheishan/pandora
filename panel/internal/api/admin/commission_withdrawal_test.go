package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewWithdrawalRejectsWhitespaceOnlyReasonBeforeTransaction(t *testing.T) {
	h := withdrawalValidationHandlers()
	r := httptest.NewRequest(http.MethodPost, "/withdrawals/id/review",
		strings.NewReader(`{"action":"reject","reason":" \t\n "}`))
	w := httptest.NewRecorder()

	h.reviewWithdrawal(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=%d body=%s",
			w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"reason"`) {
		t.Fatalf("validation response does not identify reason: %s", w.Body.String())
	}
}

func TestReviewWithdrawalRejectsApproveWithReasonBeforeTransaction(t *testing.T) {
	h := withdrawalValidationHandlers()
	r := httptest.NewRequest(http.MethodPost, "/withdrawals/id/review",
		strings.NewReader(`{"action":"approve","reason":" not applicable "}`))
	w := httptest.NewRecorder()

	h.reviewWithdrawal(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=%d body=%s",
			w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"reason"`) {
		t.Fatalf("validation response does not identify reason: %s", w.Body.String())
	}
}

func TestMarkWithdrawalPaidRejectsWhitespaceOnlyReferenceBeforeTransaction(t *testing.T) {
	h := withdrawalValidationHandlers()
	r := httptest.NewRequest(http.MethodPost, "/withdrawals/id/paid",
		strings.NewReader(`{"payout_reference":" \t\n "}`))
	w := httptest.NewRecorder()

	h.markWithdrawalPaid(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want=%d body=%s",
			w.Code, http.StatusUnprocessableEntity, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"payout_reference"`) {
		t.Fatalf("validation response does not identify payout_reference: %s", w.Body.String())
	}
}

func TestWithdrawalTextNormalizationTrimsValidValues(t *testing.T) {
	if got := normalizeWithdrawalReason("  duplicate request \t"); got != "duplicate request" {
		t.Fatalf("reason=%q", got)
	}
	if got := normalizePayoutReference("\n bank-ref-001  "); got != "bank-ref-001" {
		t.Fatalf("reference=%q", got)
	}
}

func withdrawalValidationHandlers() *handlers {
	return &handlers{d: Deps{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}}
}
