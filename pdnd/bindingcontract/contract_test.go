package bindingcontract

import (
	"strings"
	"testing"
)

func TestCheckRequestTarget(t *testing.T) {
	ok := []string{
		"/v1/servers/manifest",
		"/v1/servers/enrollments/0193f0b0-2222-7000-8000-0000000000b1/commit",
		"/v1/servers/users?pool=0193f0b0-4444-7000-8000-0000000000d1",
		"/v1/servers/agent-releases/v1.6.0/artifacts/pandora-native-linux-amd64",
		"/v1/servers/users?a=&b=x",
	}
	for _, target := range ok {
		if err := CheckRequestTarget(target); err != nil {
			t.Errorf("%q: %v", target, err)
		}
	}
	bad := []string{
		"/v1/servers/",
		"/v1/servers",
		"/v1/servers//manifest",
		"/v1/servers/manifest/",
		"/v1/servers/./manifest",
		"/v1/servers/manifest?",
		"/v1/servers/users?pool",
		"/v1/servers/users?Pool=1",
		"/v1/servers/users?a=1&a=2",
		"/v1/servers/users?pool=a b",
		"/v1/servers/users?pool=a%20b",
		"/v1/servers/users?pool=a=b",
		"/v1/servers/users#x",
		"/v1/servers/" + strings.Repeat("a", maxTargetLen),
	}
	for _, target := range bad {
		if err := CheckRequestTarget(target); err == nil {
			t.Errorf("%q: expected rejection", target)
		}
	}
}

func TestCheckRequestTimestamp(t *testing.T) {
	if err := CheckRequestTimestamp("2026-10-07T08:00:00Z"); err != nil {
		t.Fatal(err)
	}
	for _, ts := range []string{"2026-10-07T08:00:00.5Z", "2026-10-07T08:00:00+00:00", "2026-10-07 08:00:00Z", ""} {
		if err := CheckRequestTimestamp(ts); err == nil {
			t.Errorf("%q: expected rejection", ts)
		}
	}
}

func TestPins(t *testing.T) {
	k := deriveTestKeys(t)
	current := TLSPin(k.gatewaySPKI)
	next := TLSPin([]byte("next"))
	if err := CheckTLSPin(k.gatewaySPKI, next, current); err != nil {
		t.Fatalf("current pin among two must match: %v", err)
	}
	if err := CheckTLSPin(k.gatewaySPKI, next, ""); err == nil {
		t.Fatal("wrong pin must not match")
	}
	if err := CheckTLSPin(k.gatewaySPKI); err == nil {
		t.Fatal("no pin must not match")
	}
	if err := CheckTLSPin(k.gatewaySPKI, strings.TrimSuffix(current, "=")); err == nil {
		t.Fatal("unpadded pin must be rejected")
	}
	pin, err := PanelKeyFingerprint(pub(k.panelConfig))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePanelKey(strings.ToUpper(pin[len(PanelKeyPrefix):])); err == nil {
		t.Fatal("panel key without prefix must be rejected")
	}
	if _, err := ParsePanelKey(PanelKeyPrefix + strings.ToUpper(pin[len(PanelKeyPrefix):])); err == nil {
		t.Fatal("uppercase panel key must be rejected")
	}
	if _, err := PanelKeyFingerprint([]byte("short")); err == nil {
		t.Fatal("short panel key must be rejected")
	}
}

func TestSamePanelTenantNeedsTenant(t *testing.T) {
	a := PanelTenant{Origin: "https://panel.example.com:443"}
	if SamePanelTenant(a, a) {
		t.Fatal("empty tenant must never match")
	}
}

func TestWarningOnlyCodesAreFailureCodes(t *testing.T) {
	for _, code := range WarningOnlyCodes {
		found := false
		for _, c := range FailureCodes {
			found = found || c == code
		}
		if !found {
			t.Errorf("warning code %s is not a failure code", code)
		}
	}
}

func TestKeyIDMatchesExistingConfigKeyFormat(t *testing.T) {
	// 与 nodefabric 配置签名 key_id 的格式一致：sha256 前 8 字节的无填充 base64url
	k := deriveTestKeys(t)
	id := KeyID(pub(k.panelConfig))
	if err := checkKeyID("key_id", id); err != nil || len(id) != 11 {
		t.Fatalf("key id %q: %v", id, err)
	}
}
