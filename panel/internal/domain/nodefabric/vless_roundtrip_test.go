package nodefabric

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// vless / vmess / trojan 那一批（00063）的往返验证。
//
// 这是唯一一批动到**正在跑**的节点，所以口径最严：迁移之后每个节点算一次
// toKernelConfig，结果必须和迁移前逐字段相同，一个字段都不能差。
//
// 夹具是在生产数据的副本上真跑了一遍 00063 得到的，不是手推的：
// stored_xboard 是迁移之后的实际结果，expect_kernel 是迁移之前的原样。
func TestVlessMigrationRoundTripsToOriginalKernelShape(t *testing.T) {
	raw, err := os.ReadFile("testdata/vless_migration_roundtrip.json")
	if err != nil {
		t.Fatalf("读取夹具失败：%v", err)
	}
	var fixtures []struct {
		NodeNo       int            `json:"node_no"`
		NodeType     string         `json:"node_type"`
		StoredXboard map[string]any `json:"stored_xboard"`
		ExpectKernel map[string]any `json:"expect_kernel"`
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatalf("夹具不是合法 JSON：%v", err)
	}
	if len(fixtures) == 0 {
		t.Fatal("夹具是空的")
	}
	for _, f := range fixtures {
		got := toKernelConfig(f.NodeType, f.StoredXboard)
		gotJSON, _ := json.Marshal(got)
		wantJSON, _ := json.Marshal(f.ExpectKernel)
		var a, b any
		_ = json.Unmarshal(gotJSON, &a)
		_ = json.Unmarshal(wantJSON, &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("节点 %d（%s）翻译回内核形状后和迁移前不一致：\n  库里存 %s\n  翻译得 %s\n  迁移前 %s",
				f.NodeNo, f.NodeType, mustJSON(f.StoredXboard), gotJSON, wantJSON)
		}
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
