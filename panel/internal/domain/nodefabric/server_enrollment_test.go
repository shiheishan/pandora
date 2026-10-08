package nodefabric

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
)

func validServerBeginInput() ServerEnrollmentBeginInput {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	enc := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	sum := sha256.Sum256([]byte("begin"))
	return ServerEnrollmentBeginInput{
		Token: "token", RequestID: "0193f0b0-3333-7000-8000-0000000000c1", PublicKey: key,
		EncKEM: bindingcontract.EncKEM, EncPublicKey: enc, AgentVersion: "v1.6.0",
		Features: []string{"a-v1", "b-v1"}, Capabilities: json.RawMessage(`{ "protocols": ["vless"] }`),
		Hostname: " host ", BeginRequestSHA256: sum[:],
	}
}

func TestServerBeginInputValidation(t *testing.T) {
	in := validServerBeginInput()
	_, _, caps, err := in.validate()
	if err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	if string(caps) != `{"protocols":["vless"]}` || in.Hostname != "host" {
		t.Fatalf("capabilities not compacted or hostname not trimmed: %s %q", caps, in.Hostname)
	}
	cases := map[string]func(*ServerEnrollmentBeginInput){
		"uppercase request id":  func(in *ServerEnrollmentBeginInput) { in.RequestID = strings.ToUpper(in.RequestID) },
		"empty token":           func(in *ServerEnrollmentBeginInput) { in.Token = "" },
		"short public key":      func(in *ServerEnrollmentBeginInput) { in.PublicKey = "AQID" },
		"unpadded enc key":      func(in *ServerEnrollmentBeginInput) { in.EncPublicKey = strings.TrimRight(in.EncPublicKey, "=") },
		"other kem":             func(in *ServerEnrollmentBeginInput) { in.EncKEM = "x25519" },
		"git describe version":  func(in *ServerEnrollmentBeginInput) { in.AgentVersion = "v1.6.0-3-gabcdef0" },
		"dev version":           func(in *ServerEnrollmentBeginInput) { in.AgentVersion = "dev" },
		"features out of order": func(in *ServerEnrollmentBeginInput) { in.Features = []string{"b-v1", "a-v1"} },
		"duplicate feature":     func(in *ServerEnrollmentBeginInput) { in.Features = []string{"a-v1", "a-v1"} },
		"bad feature name":      func(in *ServerEnrollmentBeginInput) { in.Features = []string{"A"} },
		"capabilities array":    func(in *ServerEnrollmentBeginInput) { in.Capabilities = json.RawMessage(`[1]`) },
		"capabilities too big": func(in *ServerEnrollmentBeginInput) {
			in.Capabilities = json.RawMessage(`{"x":"` + strings.Repeat("a", maxServerCapabilitiesBytes) + `"}`)
		},
		"negative cpu":     func(in *ServerEnrollmentBeginInput) { in.CPUCores = -1 },
		"missing digest":   func(in *ServerEnrollmentBeginInput) { in.BeginRequestSHA256 = nil },
		"hostname too big": func(in *ServerEnrollmentBeginInput) { in.Hostname = strings.Repeat("h", 254) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			in := validServerBeginInput()
			mutate(&in)
			if _, _, _, err := in.validate(); err == nil {
				t.Fatal("invalid begin input accepted")
			}
		})
	}
	for _, raw := range []string{"", "null", " null "} {
		in := validServerBeginInput()
		in.Capabilities = json.RawMessage(raw)
		if _, _, caps, err := in.validate(); err != nil || caps != nil {
			t.Fatalf("capabilities %q should be stored as NULL: caps=%s err=%v", raw, caps, err)
		}
	}
}

// 绑定令牌与节点接入令牌的哈希域不同：同一串令牌在两条接入里查到的是两个不同的哈希。
func TestServerBindingTokenHashIsDomainSeparated(t *testing.T) {
	if bytes.Equal(serverBindingTokenHash("t"), bootstrapTokenHash("t", "")) ||
		bytes.Equal(serverBindingTokenHash("t"), serverBindingTokenHash("u")) {
		t.Fatal("server binding token hash is not domain separated")
	}
}

func TestRenderServerBindingCommand(t *testing.T) {
	const key = "sha256:d26309f30325073a78993f618bc93c206756cea99b386fa3086381f6ecb415d9"
	const pin = "sha256//70GcM08q6gTsnGhWGaLrJHWM4+khfPx0FiKcSrbuUcY="
	unpinned := RenderServerBindingCommand("http://127.0.0.1:8080", "", key)
	for _, want := range []string{
		"stty -echo", "IFS= read -r PANDORA_BINDING_TOKEN </dev/tty",
		"if command -v pandora-native >/dev/null 2>&1; then pandora-native bind --panel http://127.0.0.1:8080 --panel-key '" + key + "' --token-file \"$PANDORA_TOKEN_FILE\";",
		"else curl -fsSL http://127.0.0.1:8080/pdnd/install.sh | sh -s -- --panel http://127.0.0.1:8080 --panel-key",
		"rm -f \"$PANDORA_TOKEN_FILE\"; exit $PANDORA_RC",
	} {
		if !strings.Contains(unpinned, want) {
			t.Errorf("unpinned command misses %q:\n%s", want, unpinned)
		}
	}
	// 没有钉住值时绝不能出现 -k：那等于关掉证书校验
	if strings.Contains(unpinned, " -k ") || strings.Contains(unpinned, "--pin ") {
		t.Errorf("unpinned command must not skip TLS verification:\n%s", unpinned)
	}
	pinned := RenderServerBindingCommand("https://203.0.113.10:8443", pin, key)
	for _, want := range []string{
		"curl -fsS -k --pinnedpubkey '" + pin + "' https://203.0.113.10:8443/pdnd/install.sh",
		"pandora-native bind --panel https://203.0.113.10:8443 --pin '" + pin + "' --panel-key '" + key + "'",
	} {
		if !strings.Contains(pinned, want) {
			t.Errorf("pinned command misses %q:\n%s", want, pinned)
		}
	}
	if strings.Count(pinned, "--pin '") != 2 {
		t.Errorf("both bind paths must carry --pin:\n%s", pinned)
	}
	// 面板地址带 shell 元字符时整体引起来
	if quoted := RenderServerBindingCommand("https://x;rm -rf /", "", key); !strings.Contains(quoted, "'https://x;rm -rf /'") {
		t.Errorf("unsafe panel URL is not quoted:\n%s", quoted)
	}
}

// P1 只实现了 S1：不能在接入响应里声明 server-binding-v1（那表示 S2–S7 都可用，合约 §15）。
func TestPanelServerFeaturesStayEmptyInP1(t *testing.T) {
	if len(panelServerFeatures) != 0 {
		t.Fatal("P1 must not declare server-binding-v1 before S2–S7 exist")
	}
}
