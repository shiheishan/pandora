package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// listenL4Cases 是面板侧 L4 推导的用例表。
var listenL4Cases = []struct {
	NodeType string
	Config   string
	Want     string
}{
	{"vless", `{"network":"tcp"}`, "tcp"},
	{"vless", `{"network":"ws","tls":1}`, "tcp"},
	{"vless", `{"network":"xhttp"}`, "tcp"},
	{"vless", `{"network":"xhttp-h3"}`, "udp"},
	{"vless", `{"network":"mkcp"}`, "udp"},
	{"vmess", `{"network":"kcp"}`, "udp"},
	{"vmess", `{"network":"m-kcp"}`, "udp"},
	{"vmess", `{"network":"grpc"}`, "tcp"},
	{"trojan", `{"network":"mkcp"}`, "udp"},
	{"trojan", `{"network":"ws"}`, "tcp"},
	{"trojan", `{"network":"xhttp-h3"}`, "tcp"},
	{"hysteria2", `{}`, "udp"},
	{"hysteria2", `{"network":"tcp"}`, "udp"},
	{"tuic", `{}`, "udp"},
	{"juicity", `{"network":"udp"}`, "udp"},
	{"shadowsocks", `{"method":"aes-128-gcm"}`, "tcp"},
	{"shadowsocks", `{"network":"udp"}`, "udp"},
	{"mieru", `{"transport":"UDP"}`, "udp"},
	{"mieru", `{"transport":"TCP"}`, "tcp"},
	{"mieru", `{}`, "tcp"},
	{"socks", `{"network":"udp"}`, "tcp"},
	{"http", `{}`, "tcp"},
	{"naive", `{}`, "tcp"},
	{"anytls", `{}`, "tcp"},
	{"shadowtls", `{"network":"tcp"}`, "tcp"},
	{"VLESS", `{"network":" MKCP "}`, "udp"},
	{"vless", `{"network":5}`, "tcp"},
	{"mieru", `{"transport":true}`, "tcp"},
	{"vless", `[]`, "tcp"},
	{"vless", `null`, "tcp"},
	{"", `{}`, "tcp"},
}

func TestListenL4(t *testing.T) {
	for _, tc := range listenL4Cases {
		if got := ListenL4(tc.NodeType, json.RawMessage(tc.Config)); got != tc.Want {
			t.Errorf("ListenL4(%q, %s) = %s, want %s", tc.NodeType, tc.Config, got, tc.Want)
		}
	}
}

// pdnd 的 kernel/port_claims.go 用同一套规则登记端口（先到先得）。两边口径不一致时，面板放行的
// 节点会在节点上撞端口、或面板拦下其实能共存的组合。这里读 pdnd 自己的用例表
// （TestInboundPortKeyDerivation），逐条拿面板的 ListenL4 算一遍。pdnd 加了用例这里自动跟上。
func TestListenL4MatchesPdndPortClaims(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "..", "pdnd", "kernel", "port_claims_test.go"))
	if err != nil {
		t.Fatalf("read pdnd port claim cases: %v", err)
	}
	body := string(src)
	start := strings.Index(body, "func TestInboundPortKeyDerivation")
	if start < 0 {
		t.Fatal("pdnd TestInboundPortKeyDerivation not found; update this parity test")
	}
	body = body[start:]
	if end := strings.Index(body, "\n}\n"); end > 0 {
		body = body[:end]
	}
	// {"vless", map[string]any{"network": "xhttp-h3"}, "udp"} 或 {"tuic", nil, "udp"}
	row := regexp.MustCompile(`\{"([a-z0-9]+)",\s*(nil|map\[string\]any\{([^}]*)\}),\s*"(tcp|udp)"\}`)
	field := regexp.MustCompile(`"([a-z_]+)":\s*"([^"]*)"`)
	rows := row.FindAllStringSubmatch(body, -1)
	if len(rows) < 10 {
		t.Fatalf("parsed only %d pdnd cases; the table format changed, update this parity test", len(rows))
	}
	for _, r := range rows {
		cfg := map[string]any{}
		for _, f := range field.FindAllStringSubmatch(r[3], -1) {
			cfg[f[1]] = f[2]
		}
		raw, _ := json.Marshal(cfg)
		if got := ListenL4(r[1], raw); got != r[4] {
			t.Errorf("pdnd case %s %s → %s, panel ListenL4 = %s", r[1], raw, r[4], got)
		}
	}
}

func TestPortClaimLabel(t *testing.T) {
	if got := PortClaimLabel(443, ListenTCP); got != "443/TCP" {
		t.Fatalf("PortClaimLabel(443, tcp) = %q", got)
	}
	if got := PortClaimLabel(8443, ListenUDP); got != "8443/UDP" {
		t.Fatalf("PortClaimLabel(8443, udp) = %q", got)
	}
}
