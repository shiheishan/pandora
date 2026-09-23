package nodefabric

import (
	"strings"
	"testing"
)

func TestServerSelectCoalescesNullableHeartbeat(t *testing.T) {
	if !strings.Contains(serverSelect, "coalesce(s.status='ready'") {
		t.Fatal("heartbeat_online must never scan SQL NULL into a Go bool")
	}
}

func TestValidateCreateServerInput(t *testing.T) {
	in := CreateServerInput{Name: " hk-01 ", PublicIPv4: "203.0.113.8"}
	if err := ValidateCreateServerInput(&in); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if in.Name != "hk-01" || in.CapacityNodes != 32 {
		t.Fatalf("defaults not applied: %+v", in)
	}
}

func TestValidateCreateServerInputRejectsInvalid(t *testing.T) {
	for name, in := range map[string]CreateServerInput{
		"missing name": {CapacityNodes: 1},
		"bad ipv4":     {Name: "x", PublicIPv4: "not-an-ip", CapacityNodes: 1},
		"ipv4 in v6":   {Name: "x", PublicIPv6: "192.0.2.1", CapacityNodes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateCreateServerInput(&in); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidatePatchServerInputRequiresVersion(t *testing.T) {
	if err := ValidatePatchServerInput(PatchServerInput{}); err == nil {
		t.Fatal("expected row_version error")
	}
	region := "hk"
	if err := ValidatePatchServerInput(PatchServerInput{RowVersion: 3, Region: &region}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidServerStatusTransition(t *testing.T) {
	if !ValidServerStatusTransition("ready", "draining") {
		t.Fatal("ready -> draining should be allowed")
	}
	if ValidServerStatusTransition("retired", "ready") {
		t.Fatal("retired server must not return to service")
	}
	if ValidServerStatusTransition("draft", "unhealthy") {
		t.Fatal("draft -> unhealthy is not a lifecycle edge")
	}
	if ValidServerStatusTransition("ready", "maintenance") {
		t.Fatal("ready must drain before maintenance")
	}
	if ValidServerStatusTransition("unhealthy", "ready") {
		t.Fatal("unhealthy must not return directly to ready")
	}
	if !ValidServerStatusTransition("quarantined", "draining") {
		t.Fatal("quarantined -> draining should be allowed")
	}
	if !ValidServerStatusTransition("unhealthy", "retired") ||
		!ValidServerStatusTransition("quarantined", "maintenance") {
		t.Fatal("incident retirement/recovery edges must be allowed")
	}
}
