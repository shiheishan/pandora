package adminops

import (
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestGetOrderRejectsInvalidIDBeforeDatabaseAccess(t *testing.T) {
	_, err := NewService(nil).GetOrder(context.Background(), "tenant", "not-a-uuid")
	if err == nil {
		t.Fatal("invalid order ID was accepted")
	}
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) || httpErr.Code != httpx.CodeNotFound {
		t.Fatalf("error=%v, want neutral not-found", err)
	}
}

func TestGetOrderPaymentHistoryRejectsInvalidIDBeforeDatabaseAccess(t *testing.T) {
	_, err := NewService(nil).GetOrderPaymentHistory(context.Background(), "tenant", "not-a-uuid")
	if err == nil {
		t.Fatal("invalid order ID was accepted")
	}
	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) || httpErr.Code != httpx.CodeNotFound {
		t.Fatalf("error=%v, want neutral not-found", err)
	}
}
