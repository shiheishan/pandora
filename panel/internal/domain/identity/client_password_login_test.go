package identity

import (
	"context"
	"errors"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestPasswordLoginRefusesClientAudienceBeforeDatabaseAccess(t *testing.T) {
	t.Parallel()

	service := &Service{}
	_, err := service.Login(context.Background(), "tenant-not-consulted", LoginInput{
		Email:    "user@example.test",
		Password: "not-consulted",
		Audience: "client",
	})
	if err == nil {
		t.Fatal("client password login unexpectedly succeeded")
	}

	var httpErr *httpx.Error
	if !errors.As(err, &httpErr) {
		t.Fatalf("error %v is not *httpx.Error", err)
	}
	if httpErr.Code != httpx.CodeBadRequest {
		t.Fatalf("error code = %q, want %q", httpErr.Code, httpx.CodeBadRequest)
	}
}
