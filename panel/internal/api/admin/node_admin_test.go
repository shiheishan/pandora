package admin

import "testing"

func TestValidateAdminNodeID(t *testing.T) {
	if err := validateAdminNodeID("019c1234-5678-7000-8000-000000000001"); err != nil {
		t.Fatalf("valid UUID rejected: %v", err)
	}
	if err := validateAdminNodeID("../node"); err == nil {
		t.Fatal("invalid UUID accepted")
	}
}
