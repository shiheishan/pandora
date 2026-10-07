package nodefabric

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// ListenL4Cases 是面板侧 L4 推导的用例表；PG18 用例（端口门禁）拿同一张表对照生成列 listen_l4。
var ListenL4Cases = []struct {
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
	for _, tc := range ListenL4Cases {
		if got := ListenL4(tc.NodeType, json.RawMessage(tc.Config)); got != tc.Want {
			t.Errorf("ListenL4(%q, %s) = %s, want %s", tc.NodeType, tc.Config, got, tc.Want)
		}
	}
}

// pdnd 的 kernel/port_claims.go 用同一套规则登记端口（先到先得）。两边口径不一致时，面板放行的
// 节点会在节点上撞端口、或面板拦下其实能共存的组合。这里读 pdnd 自己的用例表
// （TestInboundPortKeyDerivation），逐条拿面板的 ListenL4 算一遍。pdnd 加了用例这里自动跟上。
func TestListenL4MatchesPdndPortClaims(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "pdnd", "kernel", "port_claims_test.go"))
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

func TestReservedPortPolicy(t *testing.T) {
	p := DefaultPortPolicy()
	for _, port := range []int{22, 25, 53, 80} {
		if _, err := p.checkReservedPort(port, panelHostNo); !isInvalidField(err, "server_port") {
			t.Errorf("port %d accepted: %v", port, err)
		}
	}
	for _, port := range []int{443, 5432, 6379, 9000, 9003} {
		if _, err := p.checkReservedPort(port, panelHostYes); !isInvalidField(err, "server_port") {
			t.Errorf("panel port %d accepted on the panel host: %v", port, err)
		}
		warning, err := p.checkReservedPort(port, panelHostUnknown)
		if err != nil || warning == "" {
			t.Errorf("panel port %d on an unknown host = %q, %v; want a warning only", port, warning, err)
		}
		if warning, err := p.checkReservedPort(port, panelHostNo); err != nil || warning != "" {
			t.Errorf("panel port %d on another host = %q, %v; want nothing", port, warning, err)
		}
	}
	for _, port := range []int{8443, 9004, 2053} {
		if warning, err := p.checkReservedPort(port, panelHostUnknown); err != nil || warning != "" {
			t.Errorf("port %d = %q, %v", port, warning, err)
		}
	}
}

func TestPanelHostDetection(t *testing.T) {
	svc := &Service{}
	svc.SetPortPolicy(PortPolicy{PanelHosts: []string{"Panel.Example.TEST.", "[2001:db8::1]"}})
	p := svc.portPolicy()
	for _, tc := range []struct {
		addrs []string
		want  panelHostState
	}{
		{[]string{"panel.example.test"}, panelHostYes},
		{[]string{"", "2001:DB8:0::1"}, panelHostYes},
		{[]string{"127.0.0.1"}, panelHostYes},
		{[]string{"localhost"}, panelHostYes},
		{[]string{"::1"}, panelHostYes},
		{[]string{"node.example.test", "198.51.100.9"}, panelHostNo}, // 面板表里有 IP，服务器 IP 对不上
		{[]string{"node.example.test"}, panelHostUnknown},            // 服务器没报过 IP
	} {
		if got := p.panelHost(tc.addrs); got != tc.want {
			t.Errorf("panelHost(%v) = %d, want %d", tc.addrs, got, tc.want)
		}
	}
	// 面板表里只有域名：IP 对不上也判断不了
	only := PortPolicy{PanelHosts: []string{"panel.example.test"}}
	if got := only.panelHost([]string{"198.51.100.9"}); got != panelHostUnknown {
		t.Errorf("domain-only panel hosts = %d, want unknown", got)
	}
}

func TestPortClaimConflictNamesHolder(t *testing.T) {
	err := portClaimConflict(443, ListenTCP, "东京-01")
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeConflict {
		t.Fatalf("conflict = %#v", err)
	}
	if !strings.Contains(he.Message, "443/TCP") || !strings.Contains(he.Message, "东京-01") {
		t.Fatalf("message %q should name the port and the holder", he.Message)
	}
}

func TestSameProtocolJSON(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{`{"a":1,"b":[1,2]}`, `{ "b":[1,2], "a":1 }`, true},
		{`{"a":1}`, `{"a":1.0}`, false},
		{`{"a":"x"}`, `{"a":"y"}`, false},
		{`{"a":[1,2]}`, `{"a":[2,1]}`, false},
		{`{}`, `not json`, false},
	} {
		if got := sameProtocolJSON([]byte(tc.a), []byte(tc.b)); got != tc.want {
			t.Errorf("sameProtocolJSON(%s, %s) = %v", tc.a, tc.b, got)
		}
	}
}

func isInvalidField(err error, field string) bool {
	var he *httpx.Error
	return errors.As(err, &he) && he.Code == httpx.CodeValidationFailed && he.Fields[field] != ""
}
