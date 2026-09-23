package kernel

import (
	"fmt"
	"strings"
)

// NativeSelfCheckReport is a deterministic release/startup diagnostic. It
// verifies that every protocol advertised by NativeCore has a registered
// native adapter factory. The historical "ss" alias is intentionally allowed
// in addition to the canonical shadowsocks entry.
type NativeSelfCheckReport struct {
	Kernel              string   `json:"kernel"`
	NativeOnly          bool     `json:"native_only"`
	Status              string   `json:"status"`
	CapabilityCount     int      `json:"capability_count"`
	RegisteredAdapters  []string `json:"registered_adapters"`
	MissingCapabilities []string `json:"missing_capabilities,omitempty"`
	UnexpectedAdapters  []string `json:"unexpected_adapters,omitempty"`
}

// ValidateNativeCapabilityMatrix checks a registry against the public native
// capability contract without starting any listener.
func ValidateNativeCapabilityMatrix(registry *AdapterRegistry) error {
	if registry == nil {
		return fmt.Errorf("native adapter registry is nil")
	}
	registered := make(map[string]struct{})
	for _, protocol := range registry.Types() {
		registered[strings.ToLower(strings.TrimSpace(protocol))] = struct{}{}
	}

	var missing []string
	for _, capability := range NativeCapabilities() {
		if _, ok := registered[capability.Protocol]; !ok {
			missing = append(missing, capability.Protocol)
		}
	}

	var unexpected []string
	for protocol := range registered {
		if protocol == "ss" {
			continue
		}
		if _, ok := NativeCapabilityFor(protocol); !ok {
			unexpected = append(unexpected, protocol)
		}
	}
	if len(missing) != 0 || len(unexpected) != 0 {
		return fmt.Errorf("native capability matrix drift: missing=%v unexpected=%v", missing, unexpected)
	}
	return nil
}

// NativeSelfCheckFor runs the check against the production default registry
// and returns a stable JSON-ready result. It does not open a socket.
func NativeSelfCheckFor(nativeOnly bool) NativeSelfCheckReport {
	registry := NewDefaultAdapterRegistry()
	report := NativeSelfCheckReport{
		Kernel:             "pandora-native",
		NativeOnly:         nativeOnly,
		Status:             "ok",
		CapabilityCount:    len(NativeCapabilities()),
		RegisteredAdapters: registry.Types(),
	}
	if err := ValidateNativeCapabilityMatrix(registry); err != nil {
		report.Status = "failed"
		registered := make(map[string]struct{}, len(report.RegisteredAdapters))
		for _, protocol := range report.RegisteredAdapters {
			registered[protocol] = struct{}{}
		}
		for _, capability := range NativeCapabilities() {
			if _, ok := registered[capability.Protocol]; !ok {
				report.MissingCapabilities = append(report.MissingCapabilities, capability.Protocol)
			}
		}
		for _, protocol := range report.RegisteredAdapters {
			if protocol == "ss" {
				continue
			}
			if _, ok := NativeCapabilityFor(protocol); !ok {
				report.UnexpectedAdapters = append(report.UnexpectedAdapters, protocol)
			}
		}
	}
	return report
}
