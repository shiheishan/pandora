package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

// testPSKMaster 是测试用主密钥：0x00..0x1f 递增 32 字节的 base64（虚构）。
const testPSKMaster = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="

// testPSKDeriver 返回用 testPSKMaster 构造的 *crypto.Keys。
func testPSKDeriver() PSKDeriver {
	k, err := crypto.NewKeys(testPSKMaster)
	if err != nil {
		panic(err)
	}
	return k
}

func TestSS2022ServerPSKVector(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	d := testPSKDeriver()

	got16 := base64.StdEncoding.EncodeToString(ss2022ServerPSK(d, id, 16))
	const want16 = "G4hzx91XF5eSWFwQRrU+TA=="
	if got16 != want16 {
		t.Fatalf("16 字节向量不符: got %s, want %s", got16, want16)
	}

	got32 := base64.StdEncoding.EncodeToString(ss2022ServerPSK(d, id, 32))
	const want32 = "G4hzx91XF5eSWFwQRrU+TAYU2Hb/PdR4I2vAvXmyuRg="
	if got32 != want32 {
		t.Fatalf("32 字节向量不符: got %s, want %s", got32, want32)
	}

	id2 := uuid.MustParse("00112233-4455-6677-8899-aabbccddee00")
	gotDiff := base64.StdEncoding.EncodeToString(ss2022ServerPSK(d, id2, 16))
	if gotDiff == got16 {
		t.Fatalf("换节点 id 后密钥不应相同")
	}
}

func TestApplySS2022ServerPSK(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	d := testPSKDeriver()

	t.Run("shadowsocks 2022", func(t *testing.T) {
		kernel := map[string]any{"method": "2022-blake3-aes-256-gcm"}
		if err := applySS2022ServerPSK(d, id, "shadowsocks", kernel); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		const want32 = "G4hzx91XF5eSWFwQRrU+TAYU2Hb/PdR4I2vAvXmyuRg="
		if kernel["password"] != want32 {
			t.Fatalf("password = %v, want %s", kernel["password"], want32)
		}
	})

	t.Run("shadowsocks classic drops password", func(t *testing.T) {
		kernel := map[string]any{"method": "aes-128-gcm", "password": "stray"}
		if err := applySS2022ServerPSK(d, id, "shadowsocks", kernel); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := kernel["password"]; ok {
			t.Fatalf("经典方法不应有 password 键")
		}
	})

	t.Run("shadowtls untouched", func(t *testing.T) {
		kernel := map[string]any{"method": "aes-128-gcm", "password": "stls-pass-1"}
		if err := applySS2022ServerPSK(d, id, "shadowtls", kernel); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if kernel["password"] != "stls-pass-1" {
			t.Fatalf("shadowtls password 不应被动: %v", kernel["password"])
		}
	})

	t.Run("nil deriver", func(t *testing.T) {
		kernel2022 := map[string]any{"method": "2022-blake3-aes-256-gcm"}
		err := applySS2022ServerPSK(nil, id, "shadowsocks", kernel2022)
		if !errors.Is(err, errSS2022KeysMissing) {
			t.Fatalf("2022 方法派生器为 nil 应报错 errSS2022KeysMissing，实际 got %v", err)
		}

		kernelClassic := map[string]any{"method": "aes-128-gcm", "password": "stray"}
		if err := applySS2022ServerPSK(nil, id, "shadowsocks", kernelClassic); err != nil {
			t.Fatalf("经典方法派生器为 nil 应返回 nil，实际 got %v", err)
		}
		if _, ok := kernelClassic["password"]; ok {
			t.Fatalf("经典方法仍应删掉 password 键")
		}
	})
}

func TestBuildNodeConfigDocSS2022(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	d := testPSKDeriver()

	// 2022 方法配置
	doc2022, err := buildNodeConfigDoc(id, "shadowsocks", 8388, `{"cipher":"2022-blake3-aes-128-gcm"}`, nil, nil, d)
	if err != nil {
		t.Fatalf("buildNodeConfigDoc error: %v", err)
	}
	var m2022 map[string]any
	if err := json.Unmarshal(doc2022.JSON, &m2022); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	const want16 = "G4hzx91XF5eSWFwQRrU+TA=="
	if m2022["password"] != want16 {
		t.Fatalf("password = %v, want %s", m2022["password"], want16)
	}
	if m2022["method"] != "2022-blake3-aes-128-gcm" {
		t.Fatalf("method = %v, want 2022-blake3-aes-128-gcm", m2022["method"])
	}

	// 经典方法配置
	docClassic, err := buildNodeConfigDoc(id, "shadowsocks", 8388, `{"cipher":"aes-128-gcm"}`, nil, nil, d)
	if err != nil {
		t.Fatalf("buildNodeConfigDoc error: %v", err)
	}
	var mClassic map[string]any
	if err := json.Unmarshal(docClassic.JSON, &mClassic); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if _, ok := mClassic["password"]; ok {
		t.Fatalf("经典方法的文档里不应有 password 键")
	}

	// 2022 配置传 nil 派生器返回错误
	_, err = buildNodeConfigDoc(id, "shadowsocks", 8388, `{"cipher":"2022-blake3-aes-128-gcm"}`, nil, nil, nil)
	if err == nil {
		t.Fatal("2022 配置传 nil 派生器应报错，实际为 nil")
	}
}

func TestDeliverableSS2022RelayUsesParentPSK(t *testing.T) {
	id := uuid.MustParse("00112233-4455-6677-8899-aabbccddeeff")
	d := testPSKDeriver()

	rows := []repo.DeliveryNodeRow{
		{
			ID:             id,
			Name:           "父",
			NodeType:       "shadowsocks",
			Host:           "p.example.com",
			Port:           8388,
			ProtocolConfig: `{"cipher":"2022-blake3-aes-256-gcm"}`,
			TrafficRate:    decimal.NewFromInt(1),
			HeartbeatFresh: true,
			Relays: []repo.DeliveryRelay{
				{
					Name: "中转",
					Host: "r.example.com",
					Port: 10001,
				},
			},
		},
	}

	nodes, err := deliverableFromRows(rows, nil, time.Now(), time.UTC, d)
	if err != nil {
		t.Fatalf("deliverableFromRows error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("期望得到 2 条线路，实际 got %d", len(nodes))
	}
	const want32 = "G4hzx91XF5eSWFwQRrU+TAYU2Hb/PdR4I2vAvXmyuRg="
	for i, n := range nodes {
		if n.Config["password"] != want32 {
			t.Fatalf("节点 %d (%s) Config[password] = %v, want %s", i, n.Name, n.Config["password"], want32)
		}
	}

	// 再给一条经典方法的行，它的 Config 没有 password
	classicRows := []repo.DeliveryNodeRow{
		{
			ID:             uuid.New(),
			Name:           "经典",
			NodeType:       "shadowsocks",
			Host:           "c.example.com",
			Port:           8389,
			ProtocolConfig: `{"cipher":"aes-128-gcm"}`,
			TrafficRate:    decimal.NewFromInt(1),
			HeartbeatFresh: true,
		},
	}
	classicNodes, err := deliverableFromRows(classicRows, nil, time.Now(), time.UTC, d)
	if err != nil {
		t.Fatalf("deliverableFromRows classic error: %v", err)
	}
	if len(classicNodes) != 1 {
		t.Fatalf("期望得到 1 条线路，实际 got %d", len(classicNodes))
	}
	if _, ok := classicNodes[0].Config["password"]; ok {
		t.Fatalf("经典方法线路 Config 不应有 password 键")
	}

	// 派生器为 nil 时 2022 行返回错误
	_, err = deliverableFromRows(rows, nil, time.Now(), time.UTC, nil)
	if err == nil {
		t.Fatal("2022 行传 nil 派生器应报错，实际为 nil")
	}
}
