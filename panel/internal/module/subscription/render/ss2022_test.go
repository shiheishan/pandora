package render

import (
	"encoding/base64"
	"maps"
	"reflect"
	"strings"
	"testing"
)

// ipsk16 / ipsk32 是测试用的服务端身份密钥：0x00 起递增字节的 base64（虚构）。
const (
	ipsk16 = "AAECAwQFBgcICQoLDA0ODw=="
	ipsk32 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="
)

func TestSS2022UserPSKVectors(t *testing.T) {
	const uuid = "5f8c6a2e-3b1d-4c7a-9e2f-0a1b2c3d4e5f"

	psk16 := ss2022UserPSK(uuid, 16)
	if got := base64.StdEncoding.EncodeToString(psk16); got != "LSrMru66REBR94HS3SQJUQ==" {
		t.Fatalf("keyLen 16 期望 LSrMru66REBR94HS3SQJUQ==，实际得到 %s", got)
	}

	psk32 := ss2022UserPSK(uuid, 32)
	if got := base64.StdEncoding.EncodeToString(psk32); got != "LSrMru66REBR94HS3SQJUYSSXOqFXz50ARjdL6oJuzM=" {
		t.Fatalf("keyLen 32 期望 LSrMru66REBR94HS3SQJUYSSXOqFXz50ARjdL6oJuzM=，实际得到 %s", got)
	}

	// 大写 UUID 结果相同
	upperUUID := strings.ToUpper(uuid)
	if got := base64.StdEncoding.EncodeToString(ss2022UserPSK(upperUUID, 16)); got != "LSrMru66REBR94HS3SQJUQ==" {
		t.Fatalf("大写 UUID keyLen 16 期望 LSrMru66REBR94HS3SQJUQ==，实际得到 %s", got)
	}
	if got := base64.StdEncoding.EncodeToString(ss2022UserPSK(upperUUID, 32)); got != "LSrMru66REBR94HS3SQJUYSSXOqFXz50ARjdL6oJuzM=" {
		t.Fatalf("大写 UUID keyLen 32 期望 LSrMru66REBR94HS3SQJUYSSXOqFXz50ARjdL6oJuzM=，实际得到 %s", got)
	}
}

func TestSSPassword(t *testing.T) {

	tests := []struct {
		name         string
		cfg          map[string]any
		wantPW       string
		wantReasonIn string
	}{
		{
			name:         "经典 AEAD aes-128-gcm",
			cfg:          map[string]any{"method": "aes-128-gcm"},
			wantPW:       fixtureUUID,
			wantReasonIn: "",
		},
		{
			name: "2022 aes-128 成功",
			cfg: map[string]any{
				"method":   "2022-blake3-aes-128-gcm",
				"password": ipsk16,
			},
			wantPW:       "AAECAwQFBgcICQoLDA0ODw==:X6QuNEiY8IfbAEh6xR2EmA==",
			wantReasonIn: "",
		},
		{
			name: "2022 aes-256 成功",
			cfg: map[string]any{
				"method":   "2022-blake3-aes-256-gcm",
				"password": ipsk32,
			},
			wantPW:       "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=:X6QuNEiY8IfbAEh6xR2EmP4U5BGnPA1oxvBsUcs7DSc=",
			wantReasonIn: "",
		},
		{
			name: "2022 aes-128 缺少 password",
			cfg: map[string]any{
				"method": "2022-blake3-aes-128-gcm",
			},
			wantPW:       "",
			wantReasonIn: "服务端密钥",
		},
		{
			name: "2022 aes-128 password 不是 base64",
			cfg: map[string]any{
				"method":   "2022-blake3-aes-128-gcm",
				"password": "not-valid-base64!!",
			},
			wantPW:       "",
			wantReasonIn: "服务端密钥",
		},
		{
			name: "2022 aes-128 password 长度不符（给 32 字节）",
			cfg: map[string]any{
				"method":   "2022-blake3-aes-128-gcm",
				"password": ipsk32,
			},
			wantPW:       "",
			wantReasonIn: "服务端密钥",
		},
		{
			name: "2022 chacha20 不支持多用户",
			cfg: map[string]any{
				"method":   "2022-blake3-chacha20-poly1305",
				"password": ipsk32,
			},
			wantPW:       "",
			wantReasonIn: "多用户不支持",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgCopy := maps.Clone(tt.cfg)
			gotPW, gotReason := ssPassword(tt.cfg, fixtureUUID)

			if !reflect.DeepEqual(tt.cfg, cfgCopy) {
				t.Fatalf("ssPassword 修改了传入的 cfg: 前 %v, 后 %v", cfgCopy, tt.cfg)
			}

			if gotPW != tt.wantPW {
				t.Errorf("gotPW = %q, want %q", gotPW, tt.wantPW)
			}
			if tt.wantReasonIn == "" {
				if gotReason != "" {
					t.Errorf("gotReason = %q, want empty", gotReason)
				}
			} else {
				if !strings.Contains(gotReason, tt.wantReasonIn) {
					t.Errorf("gotReason = %q, want containing %q", gotReason, tt.wantReasonIn)
				}
			}
		})
	}
}

func TestSS2022URIEncoding(t *testing.T) {
	n := Node{
		Name: "ss2022",
		Type: "shadowsocks",
		Host: "node.example.com",
		Port: 8390,
		Config: map[string]any{
			"method":   "2022-blake3-aes-128-gcm",
			"password": ipsk16,
		},
	}

	got, reason := nodeToURI(n, fixtureUUID)
	if reason != "" {
		t.Fatalf("nodeToURI 返回非空原因: %s", reason)
	}

	const want = "ss://2022-blake3-aes-128-gcm:AAECAwQFBgcICQoLDA0ODw%3D%3D%3AX6QuNEiY8IfbAEh6xR2EmA%3D%3D@node.example.com:8390#ss2022"
	if got != want {
		t.Fatalf("nodeToURI 结果不匹配:\n 期望: %s\n 实际: %s", want, got)
	}
}

func TestSS2022SkipsByFormat(t *testing.T) {
	n := Node{
		Name: "ss2022",
		Type: "shadowsocks",
		Host: "node.example.com",
		Port: 8390,
		Config: map[string]any{
			"method":   "2022-blake3-aes-128-gcm",
			"password": ipsk16,
		},
	}

	// Clash Premium 跳过
	pPrem, premReason := nodeToClash(n, fixtureUUID, true)
	if pPrem != nil {
		t.Errorf("nodeToClash (premium=true) 期望返回 nil，实际得到 %v", pPrem)
	}
	if !strings.Contains(premReason, "Clash Premium") {
		t.Errorf("premReason = %q，期望包含 Clash Premium", premReason)
	}

	// Surfboard 跳过
	sLine, surfReason := nodeToSurfboard(n, fixtureUUID)
	if sLine != "" {
		t.Errorf("nodeToSurfboard 期望返回空行，实际得到 %q", sLine)
	}
	if !strings.Contains(surfReason, "Surfboard") {
		t.Errorf("surfReason = %q，期望包含 Surfboard", surfReason)
	}

	// Clash Meta (premium=false) 不跳过
	pMeta, metaReason := nodeToClash(n, fixtureUUID, false)
	if metaReason != "" {
		t.Fatalf("nodeToClash (premium=false) 返回非空原因: %s", metaReason)
	}
	if pMeta == nil {
		t.Fatal("nodeToClash (premium=false) 返回 nil proxy")
	}

	yaml := inlineYAML(pMeta)
	if !strings.Contains(yaml, `cipher: "2022-blake3-aes-128-gcm"`) {
		t.Errorf("inlineYAML(p) 期望包含 cipher: \"2022-blake3-aes-128-gcm\"，实际输出: %s", yaml)
	}
	if !strings.Contains(yaml, `password: "AAECAwQFBgcICQoLDA0ODw==:X6QuNEiY8IfbAEh6xR2EmA=="`) {
		t.Errorf("inlineYAML(p) 期望包含 password: \"AAECAwQFBgcICQoLDA0ODw==:X6QuNEiY8IfbAEh6xR2EmA==\"，实际输出: %s", yaml)
	}
}
