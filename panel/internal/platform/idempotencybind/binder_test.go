package idempotencybind

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type binderFakeQueryer struct {
	query string
	args  []any
	row   pgx.Row
	calls int
}

func (f *binderFakeQueryer) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	f.calls++
	f.query = query
	f.args = append([]any(nil), args...)
	return f.row
}

type binderFakeRow struct {
	value string
	err   error
}

func (r binderFakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*(dest[0].(*string)) = r.value
	return nil
}

func testClaim() middleware.IdempotencyClaim {
	hash := sha256.Sum256([]byte("request"))
	return middleware.IdempotencyClaim{
		ID:           "91000000-0000-7000-8000-000000000101",
		TenantID:     "91000000-0000-7000-8000-000000000001",
		ActorID:      "91000000-0000-7000-8000-000000000011",
		Scope:        "order_create",
		StorageScope: "order_create:actor:0123456789abcdef01234567",
		Key:          "checkout-key",
		RequestHash:  hash,
		Generation:   3,
		LockedUntil:  time.Date(2026, 7, 30, 10, 11, 12, 13, time.UTC),
	}
}

func TestBindResourcePassesCompleteClaimTupleExactlyOnce(t *testing.T) {
	claim := testClaim()
	resourceID := "91000000-0000-7000-8000-000000000201"
	fake := &binderFakeQueryer{row: binderFakeRow{value: claim.ID}}
	if err := bindResource(context.Background(), fake, claim, "order", resourceID); err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("binder query count = %d", fake.calls)
	}
	if !strings.Contains(fake.query, "app.bind_idempotency_resource") ||
		strings.Contains(strings.ToUpper(fake.query), "UPDATE IDEMPOTENCY_KEYS") ||
		strings.Contains(strings.ToUpper(fake.query), "SELECT ID FROM IDEMPOTENCY_KEYS") {
		t.Fatalf("wrapper bypassed dedicated binder: %s", fake.query)
	}
	if len(fake.args) != 11 {
		t.Fatalf("binder argument count = %d", len(fake.args))
	}
	wantStrings := map[int]string{
		0: claim.ID, 1: claim.TenantID, 2: claim.ActorID, 3: claim.Scope,
		4: claim.StorageScope, 5: claim.Key, 9: "order", 10: resourceID,
	}
	for index, want := range wantStrings {
		if got, ok := fake.args[index].(string); !ok || got != want {
			t.Fatalf("argument %d = %#v, want %q", index, fake.args[index], want)
		}
	}
	if got := fake.args[6].([]byte); string(got) != string(claim.RequestHash[:]) {
		t.Fatal("request hash argument changed")
	}
	if got := fake.args[7].(int64); got != claim.Generation {
		t.Fatalf("generation = %d", got)
	}
	if got := fake.args[8].(time.Time); !got.Equal(claim.LockedUntil) {
		t.Fatalf("lease = %s", got)
	}
}

func TestBindResourceMapsOnlyZeroRowsToClaimLost(t *testing.T) {
	claim := testClaim()
	fake := &binderFakeQueryer{row: binderFakeRow{err: pgx.ErrNoRows}}
	err := bindResource(context.Background(), fake, claim, "order",
		"91000000-0000-7000-8000-000000000201")
	if !errors.Is(err, ErrIdempotencyClaimLost) || fake.calls != 1 {
		t.Fatalf("zero-row mapping/calls = %v/%d", err, fake.calls)
	}

	databaseErr := errors.New("database unavailable")
	fake = &binderFakeQueryer{row: binderFakeRow{err: databaseErr}}
	err = bindResource(context.Background(), fake, claim, "order",
		"91000000-0000-7000-8000-000000000201")
	if !errors.Is(err, databaseErr) || errors.Is(err, ErrIdempotencyClaimLost) || fake.calls != 1 {
		t.Fatalf("database error mapping/calls = %v/%d", err, fake.calls)
	}
}

func TestBindResourceRejectsUnexpectedReturnedClaim(t *testing.T) {
	claim := testClaim()
	fake := &binderFakeQueryer{row: binderFakeRow{
		value: "91000000-0000-7000-8000-000000000999",
	}}
	err := bindResource(context.Background(), fake, claim, "order",
		"91000000-0000-7000-8000-000000000201")
	if err == nil || errors.Is(err, ErrIdempotencyClaimLost) || fake.calls != 1 {
		t.Fatalf("unexpected-id result/calls = %v/%d", err, fake.calls)
	}
}

func TestCompleteSuccessJSONPassesExactClaimResourceAndResponse(t *testing.T) {
	claim := testClaim()
	claim.Scope = "balance_topup_create"
	claim.StorageScope = "balance_topup_create:actor:0123456789abcdef01234567"
	resourceID := "91000000-0000-7000-8000-000000000201"
	response, err := httpx.PrepareJSON(http.StatusOK, struct {
		OrderID string `json:"order_id"`
	}{OrderID: resourceID})
	if err != nil {
		t.Fatal(err)
	}
	fake := &binderFakeQueryer{row: binderFakeRow{value: claim.ID}}
	if err := completeSuccessJSON(
		context.Background(), fake, claim, "order", resourceID, response,
	); err != nil {
		t.Fatalf("completion failed: %v", err)
	}
	if fake.calls != 1 || len(fake.args) != 18 {
		t.Fatalf("completion calls/args = %d/%d", fake.calls, len(fake.args))
	}
	if !strings.Contains(fake.query, "app.complete_bound_idempotency_success") ||
		strings.Contains(strings.ToUpper(fake.query), "UPDATE IDEMPOTENCY_KEYS") {
		t.Fatalf("completion bypassed security-definer function: %s", fake.query)
	}
	wantStrings := map[int]string{
		0: claim.ID, 1: claim.TenantID, 2: claim.ActorID, 3: claim.Scope,
		4: claim.StorageScope, 5: claim.Key, 9: "order", 10: resourceID,
		13: "application/json; charset=utf-8",
	}
	for index, want := range wantStrings {
		if got, ok := fake.args[index].(string); !ok || got != want {
			t.Fatalf("argument %d = %#v, want %q", index, fake.args[index], want)
		}
	}
	if got := fake.args[6].([]byte); string(got) != string(claim.RequestHash[:]) {
		t.Fatal("completion request hash changed")
	}
	if got := fake.args[7].(int64); got != claim.Generation {
		t.Fatalf("completion generation = %d", got)
	}
	if got := fake.args[8].(time.Time); !got.Equal(claim.LockedUntil) {
		t.Fatalf("completion lease = %s", got)
	}
	if got := fake.args[11].(int); got != http.StatusOK {
		t.Fatalf("completion status = %d", got)
	}
	wantBody := "{\"order_id\":\"" + resourceID + "\"}\n"
	if got := fake.args[12].([]byte); string(got) != wantBody {
		t.Fatalf("completion payload = %q", got)
	}
	for index := 14; index < 18; index++ {
		if fake.args[index] != nil {
			t.Fatalf("optional replay header %d = %#v", index, fake.args[index])
		}
	}
}

func TestCompleteSuccessJSONMapsZeroRowsAndRejectsUnexpectedClaim(t *testing.T) {
	claim := testClaim()
	response, err := httpx.PrepareJSON(http.StatusOK, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	fake := &binderFakeQueryer{row: binderFakeRow{err: pgx.ErrNoRows}}
	err = completeSuccessJSON(context.Background(), fake, claim, "order",
		"91000000-0000-7000-8000-000000000201", response)
	if !errors.Is(err, ErrIdempotencyClaimLost) || fake.calls != 1 {
		t.Fatalf("zero-row completion = %v/%d", err, fake.calls)
	}

	fake = &binderFakeQueryer{row: binderFakeRow{
		value: "91000000-0000-7000-8000-000000000999",
	}}
	err = completeSuccessJSON(context.Background(), fake, claim, "order",
		"91000000-0000-7000-8000-000000000201", response)
	if err == nil || errors.Is(err, ErrIdempotencyClaimLost) || fake.calls != 1 {
		t.Fatalf("unexpected completion id = %v/%d", err, fake.calls)
	}
}
