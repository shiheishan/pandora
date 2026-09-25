package nodefabric

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPreserveRedactedProtocolSecrets(t *testing.T) {
	cases := []struct {
		name, stored, incoming, want string
	}{
		{
			name:     "抹掉的顶层密钥补回，普通字段按请求改",
			stored:   `{"network":"tcp","version":3,"password":"outer-secret","server":"a.example.com:443"}`,
			incoming: `{"network":"tcp","version":3,"server":"b.example.com:443"}`,
			want:     `{"network":"tcp","version":3,"password":"outer-secret","server":"b.example.com:443"}`,
		},
		{
			name:     "嵌套对象里的密钥按原路径补回",
			stored:   `{"network":"udp","obfs":{"type":"salamander","password":"s3cret"},"up_mbps":100}`,
			incoming: `{"network":"udp","obfs":{"type":"salamander"},"up_mbps":200}`,
			want:     `{"network":"udp","obfs":{"type":"salamander","password":"s3cret"},"up_mbps":200}`,
		},
		{
			name:     "显式给了新值以请求为准",
			stored:   `{"password":"old","server":"a"}`,
			incoming: `{"password":"new","server":"a"}`,
			want:     `{"password":"new","server":"a"}`,
		},
		{
			name:     "显式空串与 null 都算给了，照样清空",
			stored:   `{"password":"old","obfs":{"password":"old"}}`,
			incoming: `{"password":"","obfs":{"password":null}}`,
			want:     `{"password":"","obfs":{"password":null}}`,
		},
		{
			name:     "普通键缺席就是删除，不补",
			stored:   `{"password":"old","server":"a","strict":true}`,
			incoming: `{"server":"a"}`,
			want:     `{"password":"old","server":"a"}`,
		},
		{
			name:     "整个父对象缺席时不补它里面的密钥",
			stored:   `{"network":"udp","obfs":{"type":"salamander","password":"s3cret"}}`,
			incoming: `{"network":"udp"}`,
			want:     `{"network":"udp"}`,
		},
		{
			name:     "等长数组按下标补回",
			stored:   `{"users":[{"name":"a","password":"pa"},{"name":"b","password":"pb"}]}`,
			incoming: `{"users":[{"name":"a"},{"name":"b2"}]}`,
			want:     `{"users":[{"name":"a","password":"pa"},{"name":"b2","password":"pb"}]}`,
		},
		{
			name:     "数组长度变了无法对齐，不补",
			stored:   `{"users":[{"name":"a","password":"pa"}]}`,
			incoming: `{"users":[{"name":"a"},{"name":"b"}]}`,
			want:     `{"users":[{"name":"a"},{"name":"b"}]}`,
		},
		{
			name:     "大小写不同的敏感键名同样识别",
			stored:   `{"Private_Key":"k","dest":"a:443"}`,
			incoming: `{"dest":"b:443"}`,
			want:     `{"Private_Key":"k","dest":"b:443"}`,
		},
		{
			name:     "大整数不因往返变形",
			stored:   `{"psk":"k","n":12345678901234567890}`,
			incoming: `{"n":12345678901234567890}`,
			want:     `{"psk":"k","n":12345678901234567890}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := PreserveRedactedProtocolSecrets(json.RawMessage(tc.stored), json.RawMessage(tc.incoming))
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, got, tc.want) {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

// 抹敏与补回必须互逆：读接口给出的形状原样 PATCH 回去，库里的配置不变。
func TestPreserveRedactedProtocolSecretsInvertsRedaction(t *testing.T) {
	stored := json.RawMessage(`{"dest":"www.example.com:443","security":"reality","private_key":"pk","short_ids":["ab"],"obfs":{"type":"salamander","obfs-password":"x"}}`)
	got, err := PreserveRedactedProtocolSecrets(stored, RedactProtocolConfig(stored))
	if err != nil {
		t.Fatal(err)
	}
	if !sameJSON(t, got, string(stored)) {
		t.Fatalf("round trip changed config: %s", got)
	}
}

func TestPreserveRedactedProtocolSecretsLeavesBytesAlone(t *testing.T) {
	// 没东西可补、请求带重复键、库里是坏 JSON：都原样返回请求字节。
	for name, pair := range map[string][2]string{
		"nothing to restore": {`{"password":"p"}`, `{"password":"q", "a":"<b>"}`},
		"duplicate keys":     {`{"password":"p"}`, `{"a":1,"a":2}`},
		"stored not json":    {`{bad`, `{"a":1}`},
		"stored empty":       {``, `{"a":1}`},
	} {
		got, err := PreserveRedactedProtocolSecrets(json.RawMessage(pair[0]), json.RawMessage(pair[1]))
		if err != nil || string(got) != pair[1] {
			t.Fatalf("%s: got %q err=%v", name, got, err)
		}
	}
	// 补过之后也不转义 HTML 字符。
	got, _ := PreserveRedactedProtocolSecrets(json.RawMessage(`{"password":"p"}`), json.RawMessage(`{"a":"<b>"}`))
	if string(got) != `{"a":"<b>","password":"p"}` {
		t.Fatalf("escaped output: %s", got)
	}
}

func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	g, err := decodeJSONNumber(got)
	if err != nil {
		t.Fatalf("decode got %s: %v", got, err)
	}
	w, err := decodeJSONNumber([]byte(want))
	if err != nil {
		t.Fatalf("decode want %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// mask_password 挂在 mask 上：开关没变才补，关掉掩码或离开 mKCP 时不补。
// 直接拿真实校验器验证合并结果，确认三种常见编辑都能保存。
func TestPreserveRedactedProtocolSecretsGatedMaskPassword(t *testing.T) {
	stored := json.RawMessage(`{"network":"mkcp","mask":"mkcp-aes128gcm","mask_password":"kcp-pass","mtu":1200}`)
	cases := []struct {
		name, incoming string
		wantPassword   bool
	}{
		{"掩码不变只改 MTU：补回口令", `{"network":"mkcp","mask":"mkcp-aes128gcm","mtu":1100}`, true},
		{"关掉掩码：不补", `{"network":"mkcp","mask":"none","mtu":1100}`, false},
		{"去掉 mask 键：不补", `{"network":"mkcp","mtu":1100}`, false},
		{"离开 mKCP：不补", `{"network":"tcp"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, err := PreserveRedactedProtocolSecrets(stored, json.RawMessage(tc.incoming))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(merged), "kcp-pass"); got != tc.wantPassword {
				t.Fatalf("password restored=%v want %v: %s", got, tc.wantPassword, merged)
			}
			if _, err := validateNewNodeProtocol("vless", "auto", "n.invalid", 443, merged); err != nil {
				t.Fatalf("merged config rejected: %v (%s)", err, merged)
			}
		})
	}
}

func TestSecretGatesPointAtSensitiveKeys(t *testing.T) {
	for secret, gate := range secretGates {
		if _, ok := sensitiveProtocolKey[secret]; !ok {
			t.Errorf("gated secret %q is not redacted", secret)
		}
		if _, ok := sensitiveProtocolKey[gate]; ok {
			t.Errorf("gate %q must be a visible key, but it is redacted", gate)
		}
	}
}
