package nodefabric

import (
	"encoding/json"
	"strings"
	"testing"
)

// 收紧规则（protocol_validate_policy.go）的测试。用例一律走管理端入口
// ValidateAdminProtocolConfig、喂后台表单写出的 xboard 形状，错误键也按表单路径
// 断言——这是管理员真正会碰到的那一层。

const (
	policyCert = `"cert_path":"/etc/pandora-native/certs/example.com/fullchain.pem","key_path":"/etc/pandora-native/certs/example.com/privkey.pem"`
)

func policyReality(t *testing.T, extra string) string {
	t.Helper()
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	return `"reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com",` +
		`"private_key":"` + priv + `","public_key":"` + pub + `","short_id":"0a1b2c3d"` + extra + `}`
}

func adminFields(t *testing.T, nodeType, kernel, raw string) map[string]string {
	t.Helper()
	version, fields := ValidateAdminProtocolConfig(nodeType, kernel, 443, json.RawMessage(raw))
	if len(fields) == 0 && version != StableProtocolSchemaVersion {
		t.Fatalf("%s %s: accepted with version %d", nodeType, raw, version)
	}
	return fields
}

func expectAccepted(t *testing.T, nodeType, raw string) {
	t.Helper()
	if fields := adminFields(t, nodeType, "pandora-native", raw); len(fields) != 0 {
		t.Errorf("%s %s: rejected %v", nodeType, raw, fields)
	}
}

func expectRejected(t *testing.T, nodeType, raw, field, contains string) {
	t.Helper()
	fields := adminFields(t, nodeType, "pandora-native", raw)
	msg, ok := fields[field]
	if !ok {
		t.Errorf("%s %s: want error on %s, got %v", nodeType, raw, field, fields)
		return
	}
	if contains != "" && !strings.Contains(msg, contains) {
		t.Errorf("%s %s: %s = %q, want it to mention %q", nodeType, raw, field, msg, contains)
	}
}

func TestFallbackAcceptsPublicHostPort(t *testing.T) {
	for _, fallback := range []string{"www.example.com:80", "203.0.113.10:8080", "[2001:db8::1]:80", "site.example.org:443"} {
		expectAccepted(t, "trojan", `{"tls":1,"network":"tcp",`+policyCert+`,"fallback":"`+fallback+`"}`)
		expectAccepted(t, "anytls", `{"tls":true,`+policyCert+`,"fallback":"`+fallback+`"}`)
		expectAccepted(t, "naive", `{"tls":true,`+policyCert+`,"fallback":"`+fallback+`"}`)
	}
	// REALITY 的 Trojan 也能配：REALITY 层认证过了、Trojan 口令不对时回落
	expectAccepted(t, "trojan", `{"tls":2,"network":"tcp",`+policyReality(t, "")+`,"fallback":"www.example.com:80"}`)
	// VLESS REALITY + tcp（w4kernel 起 pdnd 读 fallback）：UUID 不对时回落
	for _, fallback := range []string{"www.example.com:80", "203.0.113.10:8080", "127.0.0.1:80"} {
		expectAccepted(t, "vless", `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision",`+policyReality(t, "")+`,"fallback":"`+fallback+`"}`)
	}
	expectAccepted(t, "vless", `{"tls":2,"network":"tcp",`+policyReality(t, "")+`,"fallback":""}`)
	// 空串等于没配
	expectAccepted(t, "anytls", `{"tls":true,`+policyCert+`,"fallback":""}`)
}

func TestFallbackAllowsLocalLoopback(t *testing.T) {
	// 用户 2026-10-07 定：回落到本机 nginx 是常见部署，本机回环放行
	for _, fallback := range []string{"127.0.0.1:80", "[::1]:80", "localhost:8080", "[::ffff:127.0.0.1]:80"} {
		expectAccepted(t, "trojan", `{"tls":1,"network":"tcp",`+policyCert+`,"fallback":"`+fallback+`"}`)
		expectAccepted(t, "anytls", `{"tls":true,`+policyCert+`,"fallback":"`+fallback+`"}`)
	}
}

func TestFallbackRejectsURLsPathsAndInternalTargets(t *testing.T) {
	cases := map[string]string{
		"http://www.example.com:80": "http://",
		"www.example.com:80/path":   "路径",
		"user@www.example.com:80":   "",
		"www.example.com":           "host:port",
		"www.example.com:0":         "端口",
		"www.example.com:70000":     "端口",
		"web.localhost:80":          "localhost",
		"nas.local:80":              "local",
		"10.0.0.5:80":               "私网",
		"192.168.1.1:80":            "私网",
		"172.16.0.1:80":             "私网",
		"100.64.0.1:80":             "私网",
		"[fd00::1]:80":              "私网",
		"169.254.169.254:80":        "链路本地",
		"[fe80::1]:80":              "链路本地",
		"0.0.0.0:80":                "0.0.0.0",
		"nginx:80":                  "单个标签",
		"router.home.arpa:80":       "home.arpa",
		"svc.cluster.internal:80":   "internal",
		"bad_host.example.com:80":   "域名",
	}
	for fallback, mention := range cases {
		t.Run(fallback, func(t *testing.T) {
			expectRejected(t, "trojan", `{"tls":1,"network":"tcp",`+policyCert+`,"fallback":"`+fallback+`"}`, "protocol_config.fallback", mention)
			expectRejected(t, "anytls", `{"tls":true,`+policyCert+`,"fallback":"`+fallback+`"}`, "protocol_config.fallback", mention)
			expectRejected(t, "naive", `{"tls":true,`+policyCert+`,"fallback":"`+fallback+`"}`, "protocol_config.fallback", mention)
			expectRejected(t, "vless", `{"tls":2,"network":"tcp",`+policyReality(t, "")+`,"fallback":"`+fallback+`"}`, "protocol_config.fallback", mention)
		})
	}
}

func TestFallbackOnlyWhereNodeAgentHonoursIt(t *testing.T) {
	// Trojan 只在 tcp 上回落；ws 等传输上 pdnd 忽略这个键
	expectRejected(t, "trojan", `{"tls":1,"network":"ws",`+policyCert+`,"fallback":"www.example.com:80"}`, "protocol_config.fallback", "tcp")
	// 其它协议没有回落：socks / http 是同一个校验分支，要单独拒
	expectRejected(t, "socks", `{"network":"tcp","fallback":"www.example.com:80"}`, "protocol_config.fallback", "")
	expectRejected(t, "http", `{"fallback":"www.example.com:80"}`, "protocol_config.fallback", "")
	// VLESS 同 Trojan：只在 tcp 承载上回落，ws / grpc / xhttp 上 pdnd 不读
	for _, network := range []string{"ws", "grpc", "xhttp"} {
		expectRejected(t, "vless", `{"tls":2,"network":"`+network+`",`+policyReality(t, "")+`,"fallback":"www.example.com:80"}`, "protocol_config.fallback", "tcp")
	}
	expectRejected(t, "vless", `{"tls":0,"network":"ws","fallback":"www.example.com:80"}`, "protocol_config.fallback", "tcp")
	// VMess 没有回落
	expectRejected(t, "vmess", `{"tls":0,"network":"ws","fallback":"www.example.com:80"}`, "protocol_config.fallback", "VMess")
}

func TestFallbackIsDeliveredVerbatim(t *testing.T) {
	svc := &Service{}
	body, _, err := svc.BuildNodeConfig(&ServingNode{
		Name: "t", NodeType: "trojan", ServerPort: 443, Kernel: "auto",
		Protocol: json.RawMessage(`{"tls":1,"network":"tcp",` + policyCert + `,"fallback":"www.example.com:80"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["fallback"] != "www.example.com:80" {
		t.Fatalf("fallback = %#v, want the host:port as configured", got["fallback"])
	}
	// VLESS 同样原样下发给节点（pdnd vless.go 用 parseProbeFallback 读）
	body, _, err = svc.BuildNodeConfig(&ServingNode{
		Name: "v", NodeType: "vless", ServerPort: 443, Kernel: "auto",
		Protocol: json.RawMessage(`{"tls":2,"network":"tcp","flow":"xtls-rprx-vision",` + policyReality(t, "") + `,"fallback":"127.0.0.1:80"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["fallback"] != "127.0.0.1:80" {
		t.Fatalf("vless fallback = %#v, want the host:port as configured", got["fallback"])
	}
}

func TestPlaintextTCPIsRejectedButCDNTransportsStay(t *testing.T) {
	expectRejected(t, "vless", `{"tls":0,"network":"tcp"}`, "protocol_config.network", "明文")
	expectRejected(t, "vless", `{"tls":0}`, "protocol_config.network", "明文")
	expectRejected(t, "vless", `{}`, "protocol_config.network", "明文")
	// VMess 自带加密：裸 tcp 放行，读接口给提示（用户 2026-10-07 定）
	expectAccepted(t, "vmess", `{"tls":0,"network":"tcp"}`)
	expectAccepted(t, "vmess", `{}`)
	if w := ProtocolConfigWarnings("vmess", json.RawMessage(`{"network":"tcp"}`)); len(w) != 1 || !strings.Contains(w[0], "容易被识别") {
		t.Fatalf("vmess 裸 tcp 应有提示，得到 %v", w)
	}
	if w := ProtocolConfigWarnings("vmess", json.RawMessage(`{"network":"ws"}`)); len(w) != 0 {
		t.Fatalf("vmess ws 不应提示，得到 %v", w)
	}
	for _, nodeType := range []string{"vless", "vmess"} {
		for _, network := range []string{"ws", "httpupgrade", "grpc", "xhttp", "mkcp"} {
			expectAccepted(t, nodeType, `{"tls":0,"network":"`+network+`"}`)
		}
	}
}

func TestPlainTLSGivesAReadableMessage(t *testing.T) {
	for _, nodeType := range []string{"vless", "vmess"} {
		expectRejected(t, nodeType, `{"tls":1,"network":"tcp"}`, "protocol_config.tls", "证书自动申请上线后开放")
		expectRejected(t, nodeType, `{"tls":7,"network":"ws"}`, "protocol_config.tls", "0（不加密）或 2（REALITY）")
	}
}

func TestRealityNetworkWhitelist(t *testing.T) {
	for _, network := range []string{"tcp", "grpc", "xhttp"} {
		expectAccepted(t, "vless", `{"tls":2,"network":"`+network+`",`+policyReality(t, "")+`}`)
	}
	for _, network := range []string{"mkcp", "ws", "httpupgrade", "xhttp-h3"} {
		expectRejected(t, "vless", `{"tls":2,"network":"`+network+`",`+policyReality(t, "")+`}`, "protocol_config.network", "REALITY")
	}
}

func TestFlowIsAnEnumAndNeedsRealityTCP(t *testing.T) {
	expectAccepted(t, "vless", `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision",`+policyReality(t, "")+`}`)
	expectAccepted(t, "vless", `{"tls":2,"network":"tcp","flow":"xtls-rprx-vision-udp443",`+policyReality(t, "")+`}`)
	expectRejected(t, "vless", `{"tls":2,"network":"tcp","flow":"xtls-rprx-visoin",`+policyReality(t, "")+`}`, "protocol_config.flow", "xtls-rprx-vision")
	expectRejected(t, "vless", `{"tls":2,"network":"tcp","flow":"xtls-rprx-direct",`+policyReality(t, "")+`}`, "protocol_config.flow", "")
	expectRejected(t, "vless", `{"tls":2,"network":"grpc","flow":"xtls-rprx-vision",`+policyReality(t, "")+`}`, "protocol_config.flow", "REALITY + tcp")
	expectRejected(t, "vless", `{"tls":0,"network":"ws","flow":"xtls-rprx-vision"}`, "protocol_config.flow", "REALITY + tcp")
	expectRejected(t, "vmess", `{"tls":0,"network":"ws","flow":"xtls-rprx-vision"}`, "protocol_config.flow", "VMess")
}

func TestUTLSIsAnEnum(t *testing.T) {
	for _, fp := range utlsFingerprints {
		expectAccepted(t, "vless", `{"tls":2,"network":"tcp","utls":"`+fp+`",`+policyReality(t, "")+`}`)
		expectAccepted(t, "trojan", `{"tls":1,"network":"tcp","utls":"`+fp+`",`+policyCert+`}`)
		expectAccepted(t, "anytls", `{"tls":true,"utls":"`+fp+`",`+policyCert+`}`)
	}
	for _, typo := range []string{"chorme", "Chrome ", "randomized", "unsafe"} {
		expectRejected(t, "vless", `{"tls":2,"network":"tcp","utls":"`+typo+`",`+policyReality(t, "")+`}`, "protocol_config.utls", "chrome")
		expectRejected(t, "trojan", `{"tls":1,"network":"tcp","utls":"`+typo+`",`+policyCert+`}`, "protocol_config.utls", "chrome")
		expectRejected(t, "anytls", `{"tls":true,"utls":"`+typo+`",`+policyCert+`}`, "protocol_config.utls", "chrome")
	}
}

func TestRealityShortIDIsRequired(t *testing.T) {
	priv, pub, err := GenerateRealityKeypair()
	if err != nil {
		t.Fatal(err)
	}
	base := `"dest":"www.example.com:443","server_name":"www.example.com","private_key":"` + priv + `","public_key":"` + pub + `"`
	expectRejected(t, "vless", `{"tls":2,"network":"tcp","reality_settings":{`+base+`}}`, "protocol_config.reality_settings.short_id", "必填")
	expectRejected(t, "vless", `{"tls":2,"network":"tcp","reality_settings":{`+base+`,"short_id":""}}`, "protocol_config.reality_settings.short_id", "必填")
	expectRejected(t, "vless", `{"tls":2,"network":"tcp","reality_settings":{`+base+`,"short_id":["0a1b",""]}}`, "protocol_config.reality_settings.short_id", "不能为空")
	expectRejected(t, "trojan", `{"tls":2,"network":"tcp","reality_settings":{`+base+`}}`, "protocol_config.reality_settings.short_id", "必填")
	// 多个 short id、多个 server name 都能存（订阅按用户分散）
	expectAccepted(t, "vless", `{"tls":2,"network":"tcp","reality_settings":{"dest":"www.example.com:443",`+
		`"server_name":["www.example.com","static.example.com"],"private_key":"`+priv+`","public_key":"`+pub+`","short_id":["0a1b","2c3d4e5f"]}}`)
}

func TestRealityServerNamesMustBeHostnames(t *testing.T) {
	for _, bad := range []string{"a.example.com,b.example.com", "1.1.1.1", "bad_name.example.com"} {
		expectRejected(t, "vless", `{"tls":2,"network":"tcp",`+strings.Replace(policyReality(t, ""), `"server_name":"www.example.com"`, `"server_name":"`+bad+`"`, 1)+`}`,
			"protocol_config.reality_settings.server_name", "纯域名")
	}
}

func TestRealityDestMustBePublic(t *testing.T) {
	for dest, mention := range map[string]string{
		"localhost:443":       "localhost",
		"LOCALHOST:6379":      "localhost",
		"admin.localhost:443": "localhost",
		"printer.local:443":   "local",
		"db.internal:5432":    "internal",
		"redis:6379":          "单个标签",
		"127.0.0.1:443":       "IP",
		"[::1]:443":           "IP",
		"10.1.2.3:443":        "IP",
	} {
		raw := `{"tls":2,"network":"tcp",` + strings.Replace(policyReality(t, ""), `"dest":"www.example.com:443"`, `"dest":"`+dest+`"`, 1) + `}`
		expectRejected(t, "vless", raw, "protocol_config.reality_settings.dest", mention)
		expectRejected(t, "trojan", raw, "protocol_config.reality_settings.dest", mention)
	}
}

func TestCertificatePathsLiveInTheCertificateDirectory(t *testing.T) {
	good := []string{
		"/etc/pandora-native/certs/example.com/fullchain.pem",
		"/etc/pandora-native/certs/a.pem",
	}
	for _, p := range good {
		if problem := certificatePathProblem(p); problem != "" {
			t.Errorf("%s: %s", p, problem)
		}
	}
	bad := map[string]string{
		"c.pem":                               "绝对路径",
		"/etc/ssl/c.pem":                      NodeCertificateDir,
		"/etc/pandora-native/certs/":          "规范",
		"/etc/pandora-native/certs/../shadow": "..",
		"/etc/pandora-native/certs//a.pem":    "规范",
		"/etc/pandora-native/certs/a/./b.pem": "规范",
		"/etc/pandora-native/certsx/a.pem":    NodeCertificateDir,
		"/etc/pandora-native/certs/a|b.pem":   "|",
		" /etc/pandora-native/certs/a.pem":    "空白",
	}
	for p, mention := range bad {
		if problem := certificatePathProblem(p); !strings.Contains(problem, mention) {
			t.Errorf("%q: problem %q, want it to mention %q", p, problem, mention)
		}
	}
	// 每个带证书的协议都走这条规则，错误落在表单的 cert_path / key_path 上
	for nodeType, raw := range map[string]string{
		"trojan":    `{"tls":1,"network":"tcp","cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"anytls":    `{"tls":true,"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"naive":     `{"tls":true,"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"hysteria2": `{"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"tuic":      `{"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"juicity":   `{"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"socks":     `{"tls":true,"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"http":      `{"tls":true,"cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
		"vless":     `{"tls":0,"network":"xhttp-h3","cert_path":"/etc/ssl/c.pem","key_path":"k.pem"}`,
	} {
		fields := adminFields(t, nodeType, "pandora-native", raw)
		if !strings.Contains(fields["protocol_config.cert_path"], NodeCertificateDir) || !strings.Contains(fields["protocol_config.key_path"], "绝对路径") {
			t.Errorf("%s: fields=%v", nodeType, fields)
		}
	}
}

func TestProtocolConfigWarningsFlagLegacyCertificatePaths(t *testing.T) {
	if got := ProtocolConfigWarnings("hysteria2", json.RawMessage(`{`+policyCert+`}`)); len(got) != 0 {
		t.Fatalf("compliant paths warned: %v", got)
	}
	got := ProtocolConfigWarnings("trojan", json.RawMessage(`{"tls":1,"cert_path":"/etc/ssl/c.pem","key_path":"/etc/pandora-native/certs/k.pem"}`))
	if len(got) != 1 || !strings.Contains(got[0], "cert_path") || !strings.Contains(got[0], "照常服务") {
		t.Fatalf("warnings = %v, want one for cert_path", got)
	}
	if got := ProtocolConfigWarnings("shadowsocks", json.RawMessage(`not json`)); got != nil {
		t.Fatalf("malformed config produced warnings: %v", got)
	}
}

func TestKernelOnlyAcceptsNativeChoices(t *testing.T) {
	for _, kernel := range []string{"", "auto", "pandora-native"} {
		if fields := adminFields(t, "shadowsocks", kernel, `{"cipher":"aes-256-gcm"}`); len(fields) != 0 {
			t.Errorf("kernel %q rejected: %v", kernel, fields)
		}
	}
	for _, kernel := range []string{"sing-box", "xray-core", "native", "whatever"} {
		fields := adminFields(t, "shadowsocks", kernel, `{"cipher":"aes-256-gcm"}`)
		if !strings.Contains(fields["kernel"], "NativeCore") {
			t.Errorf("kernel %q: fields=%v", kernel, fields)
		}
	}
	for in, want := range map[string]string{"": "auto", "auto": "auto", "pandora-native": "pandora-native", "sing-box": "auto", "xray-core": "auto"} {
		if got := EffectiveKernel(in); got != want {
			t.Errorf("EffectiveKernel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSchemaEnumsMatchPolicyLists(t *testing.T) {
	byType := map[string]ProtocolSchema{}
	for _, schema := range ProtocolSchemas() {
		byType[schema.NodeType] = schema
	}
	for _, nodeType := range []string{"vless", "vmess", "trojan", "anytls"} {
		if got := byType[nodeType].Enums["utls"]; strings.Join(got, ",") != strings.Join(utlsFingerprints, ",") {
			t.Errorf("%s utls enum = %v", nodeType, got)
		}
	}
	if got := byType["vless"].Enums["flow"]; strings.Join(got, ",") != strings.Join(vlessFlows, ",") {
		t.Errorf("vless flow enum = %v", got)
	}
	for _, nodeType := range []string{"trojan", "anytls", "naive", "vless"} {
		schema := byType[nodeType]
		if !containsString(schema.AllowedProperties, "fallback") || !strings.Contains(schema.Hints["fallback"], "明文 HTTP 站点") {
			t.Errorf("%s: fallback missing from schema or hint: %v / %q", nodeType, schema.AllowedProperties, schema.Hints["fallback"])
		}
	}
	for _, schema := range byType {
		if containsString(schema.AllowedProperties, "cert_path") && !strings.Contains(schema.Hints["cert_path"], NodeCertificateDir) {
			t.Errorf("%s: cert_path has no directory hint", schema.NodeType)
		}
		for path := range schema.Hints {
			if !containsString(schema.AllowedProperties, path) {
				t.Errorf("%s: hint for unknown property %q", schema.NodeType, path)
			}
		}
	}
}
