package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestAdminCancelOrderRejectsInvalidInputBeforeDatabaseAccess(t *testing.T) {
	validActor := "11111111-1111-4111-8111-111111111111"
	validOrder := "22222222-2222-4222-8222-222222222222"
	for name, input := range map[string]AdminCancelOrderInput{
		"invalid actor": {OrderID: validOrder, ActorID: "bad", ExpectedStateVersion: 1, Reason: "客户确认取消"},
		"invalid order": {OrderID: "bad", ActorID: validActor, ExpectedStateVersion: 1, Reason: "客户确认取消"},
		"missing CAS":   {OrderID: validOrder, ActorID: validActor, ExpectedStateVersion: 0, Reason: "客户确认取消"},
		"short reason":  {OrderID: validOrder, ActorID: validActor, ExpectedStateVersion: 1, Reason: "取消"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewService(nil, nil).AdminCancelOrder(context.Background(), "tenant", input)
			if err == nil {
				t.Fatal("invalid input was accepted")
			}
			var httpErr *httpx.Error
			if !errors.As(err, &httpErr) || (httpErr.Code != httpx.CodeBadRequest && httpErr.Code != httpx.CodeNotFound) {
				t.Fatalf("error=%v, want safe client error", err)
			}
		})
	}
}
