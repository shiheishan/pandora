package kernel

import "testing"

func TestValidateNativeCapabilityMatrix(t *testing.T) {
	registry := NewDefaultAdapterRegistry()
	if err := ValidateNativeCapabilityMatrix(registry); err != nil {
		t.Fatal(err)
	}
	if got := len(registry.Types()); got != len(NativeCapabilities())+1 {
		t.Fatalf("registered adapter count=%d, want %d including ss alias", got, len(NativeCapabilities())+1)
	}
}

func TestValidateNativeCapabilityMatrixRejectsDrift(t *testing.T) {
	registry := NewAdapterRegistry()
	if err := registry.Register("vless", func(InboundSpec) (Adapter, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	if err := ValidateNativeCapabilityMatrix(registry); err == nil {
		t.Fatal("incomplete registry unexpectedly passed capability validation")
	}
}

func TestNativeSelfCheckReport(t *testing.T) {
	report := NativeSelfCheckFor(true)
	if report.Status != "ok" || !report.NativeOnly || report.Kernel != "pandora-native" {
		t.Fatalf("unexpected self-check report: %+v", report)
	}
	if report.CapabilityCount != len(NativeCapabilities()) {
		t.Fatalf("capability count=%d, want %d", report.CapabilityCount, len(NativeCapabilities()))
	}
	if len(report.MissingCapabilities) != 0 || len(report.UnexpectedAdapters) != 0 {
		t.Fatalf("self-check reported drift: %+v", report)
	}
}
