// [INPUT]: 依赖本包 toKernelConfig 与 testdata/production_protocol_configs.json（生产结构 + 虚构机密）
// [OUTPUT]: xboard 字段名迁移（00062）与下发翻译互逆的契约测试
// [POS]: nodefabric 改名工程的安全网，与 vless_roundtrip_test.go 共用同一批虚构 REALITY 密钥对
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

// 这是整个改名工程的安全网。
//
// protocol_config 原来是原样平铺下发给节点的，字段名就是和内核的通信
// 契约。改名之后库里存 xboard 名，下发前由 toKernelConfig 翻译回来。
// 库里的迁移（00062）和这层翻译必须是严格互逆的一对，对不上的后果不是
// 报错，是节点拿到一份缺字段的配置然后静默连不上。
//
// 夹具的结构取自生产库导出的真实前后配对：stored_xboard 是迁移之后库里
// 实际存的，expect_kernel 是迁移之前那一份。翻译一遍必须回到原样，也就
// 证明了「换了字段名，但节点收到的东西一字未变」。
//
// 机密值已换成虚构的：REALITY 密钥对是按 GenerateRealityKeypair 同规格新生成的，
// ShadowTLS 的 password / server_key 是 fake 字样。仓库公开，夹具里只许有假密钥；
// 以后再从生产导出新样本，先替换机密字段再入库（提交前的 gitleaks 会拦真密钥）。
//
// 用真实数据而不是手写样例：这次好几个坑都是只有真实数据里才有、我没
// 想到的——naive 的 masquerade 嵌套对象、tls 存成整数 1 的 vless、
// hysteria2 的 obfs 本来就已经是 {type,password} 对象。
func TestMigratedConfigsTranslateBackToOriginalKernelShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/production_protocol_configs.json")
	if err != nil {
		t.Fatalf("读取生产配置夹具失败：%v", err)
	}
	var fixtures []struct {
		NodeType     string         `json:"node_type"`
		StoredXboard map[string]any `json:"stored_xboard"`
		ExpectKernel map[string]any `json:"expect_kernel"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("夹具不是合法 JSON：%v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("夹具是空的，这条测试就什么都没守住")
	}

	for _, f := range fixtures {
		got := toKernelConfig(f.NodeType, f.StoredXboard)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(f.ExpectKernel)
		var a, b any
		_ = json.Unmarshal(gotJSON, &a)
		_ = json.Unmarshal(wantJSON, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s 翻译回内核形状后和迁移前不一致，节点收到的配置会变："+
				"\n  库里存的 %v\n  翻译得到 %s\n  迁移前是 %s",
				f.NodeType, f.StoredXboard, gotJSON, wantJSON)
		}
	}
}

// 嵌套对象里只有 xboard 的分组容器能被摊平。
//
// naive 的 masquerade 是内核要的完整对象，摊成 masquerade_url 之类之后
// 内核读不到 masquerade 就当没配——伪装静默失效。这条单独守住，因为
// 上面那条依赖生产数据里恰好有 naive 节点，哪天它被退役就没人守了。
func TestNonContainerObjectsSurviveTranslation(t *testing.T) {
	cfg := map[string]any{
		"tls": true,
		"masquerade": map[string]any{
			"url": "http://example.com", "type": "proxy", "rewrite_host": true,
		},
	}
	got := toKernelConfig("naive", cfg)
	m, ok := got["masquerade"].(map[string]any)
	if !ok {
		t.Fatalf("masquerade 被摊平了，内核将读不到它：%#v", got)
	}
	if m["url"] != "http://example.com" || m["type"] != "proxy" {
		t.Errorf("masquerade 内容被改动：%#v", m)
	}
}

// 反过来：xboard 形状进来，要能翻译成内核字段。
// 上面几条都在证明「不该动的没动」，这条证明「该动的动了」——
// 少了它，一个什么都不做的空函数也能让前面全过。
func TestXboardShapeTranslatesToKernelFields(t *testing.T) {
	cases := []struct {
		name     string
		nodeType string
		in       map[string]any
		want     map[string]any
	}{
		{
			name:     "shadowsocks 的 cipher 变回 method",
			nodeType: "shadowsocks",
			in:       map[string]any{"cipher": "aes-256-gcm"},
			want:     map[string]any{"method": "aes-256-gcm"},
		},
		{
			name:     "hysteria2 的带宽摊平改名，obfs 对象原样保留",
			nodeType: "hysteria2",
			in: map[string]any{
				"bandwidth": map[string]any{"up": float64(100), "down": float64(200)},
				"obfs":      map[string]any{"type": "salamander", "password": "s3cret"},
			},
			// obfs 必须整个留着：内核校验器读的是 Obfs map[string]any，
			// 还要求 type == salamander。压扁它等于把配置改坏。
			want: map[string]any{
				"up_mbps": float64(100), "down_mbps": float64(200),
				"obfs": map[string]any{"type": "salamander", "password": "s3cret"},
			},
		},
		{
			name:     "mieru 的大写 transport 转小写",
			nodeType: "mieru",
			in:       map[string]any{"transport": "TCP"},
			want:     map[string]any{"transport": "tcp"},
		},
		{
			name:     "vless 的 tls=2 变成 security=reality 且不留 tls 键",
			nodeType: "vless",
			in: map[string]any{
				"tls":     float64(2),
				"network": "tcp",
				"reality_settings": map[string]any{
					"public_key": "pk", "short_id": "abcd1234", "server_name": "www.apple.com",
				},
			},
			// tls 键整个删掉，不留 tls:false。
			//
			// 校验器明确禁止 tls=true（"证书生命周期完成前仅允许 tls=false，
			// 请改用 security=reality"），而现网 4 个在役 vless 存的就是
			// 「只有 security、没有 tls」。补一个 false 会改变下发内容，
			// 也就没法再断言这次改名对数据面完全无影响。
			want: map[string]any{
				"security": "reality", "network": "tcp",
				"public_key": "pk", "short_ids": []string{"abcd1234"},
				"server_names": []string{"www.apple.com"},
			},
		},
		{
			name:     "vless 的传输参数从 network_settings 摊出来",
			nodeType: "vless",
			in: map[string]any{
				"network": "ws",
				"network_settings": map[string]any{
					"path":    "/ray",
					"headers": map[string]any{"Host": "cdn.example.com"},
				},
			},
			want: map[string]any{
				"network": "ws", "path": "/ray", "host": "cdn.example.com",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := toKernelConfig(c.nodeType, c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("翻译结果不对\n  得到 %#v\n  期望 %#v", got, c.want)
			}
		})
	}
}

func TestTrojanSchemaUsesXboardNestedShape(t *testing.T) {
	var schema *ProtocolSchema
	for _, candidate := range ProtocolSchemas() {
		if candidate.NodeType == "trojan" && candidate.Status == "stable" {
			copy := candidate
			schema = &copy
			break
		}
	}
	if schema == nil {
		t.Fatal("stable trojan schema is missing")
	}

	wantAllowed := []string{
		"network", "tls", "utls",
		"network_settings.path", "network_settings.headers.Host",
		"network_settings.serviceName", "network_settings.mode",
		"ws_path", "grpc_path", "cert_path", "key_path",
		"reality_settings.dest", "reality_settings.server_name",
		"reality_settings.private_key", "reality_settings.public_key",
		"reality_settings.short_id", "flow",
	}
	for _, property := range wantAllowed {
		if !containsString(schema.AllowedProperties, property) {
			t.Errorf("trojan schema must expose xboard property %q; got %v", property, schema.AllowedProperties)
		}
	}
	for _, legacy := range []string{"security", "dest", "server_names", "private_key", "public_key", "short_ids", "fingerprint"} {
		if containsString(schema.AllowedProperties, legacy) {
			t.Errorf("trojan schema must not expose legacy flat property %q", legacy)
		}
	}
	if got := schema.PropertyTypes["tls"]; got != "number" {
		t.Errorf("trojan tls property type = %q, want number", got)
	}
	if got := schema.Enums["tls"]; !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Errorf("trojan tls enum = %#v, want [1 2]", got)
	}
}

func TestTrojanXboardRealityConfigPassesAdminValidation(t *testing.T) {
	key := strings.Repeat("A", 43) // 32 字节的无填充 base64url 编码，仅用于测试
	raw := json.RawMessage(`{"network":"tcp","tls":2,"reality_settings":{"dest":"www.example.com:443","server_name":"www.example.com","private_key":"` + key + `","public_key":"` + key + `","short_id":"01234567"}}`)
	version, fields := ValidateAdminProtocolConfig("trojan", "pandora-native", 443, raw)
	if version != 1 || len(fields) != 0 {
		t.Fatalf("xboard-shaped Trojan REALITY config rejected: version=%d fields=%v", version, fields)
	}
}

func TestTrojanXboardTLSConfigPassesAdminValidation(t *testing.T) {
	raw := json.RawMessage(`{"network":"tcp","tls":1,"cert_path":"/etc/pandora/trojan.crt","key_path":"/etc/pandora/trojan.key"}`)
	version, fields := ValidateAdminProtocolConfig("trojan", "pandora-native", 443, raw)
	if version != 1 || len(fields) != 0 {
		t.Fatalf("xboard-shaped Trojan TLS config rejected: version=%d fields=%v", version, fields)
	}
}
