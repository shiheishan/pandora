package service

// 由 .agents/skills/subscription-e2e/scripts/run.sh 经 `go test -overlay` 注入 service 包，不落仓库。
//
// 读 E2E_OUT/fixtures_index.json，对每个夹具调用 buildNodeConfigDoc，写出 node_config.json 与
// node_config_errors.json。这是面板经 gRPC 下发给节点的配置文档（协议字段经 KernelConfig 摊平，
// 加 server_port、kernel、protocol）；节点端原样当 InboundConfig.Raw。无路由层（layers 传 nil）。
// 依赖 buildNodeConfigDoc、NodeConfigDoc。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestE2ENodeConfig(t *testing.T) {
	out := os.Getenv("E2E_OUT")
	if out == "" {
		t.Skip("E2E_OUT 未设置：这个测试只由 subscription-e2e 的 run.sh 调用")
	}
	raw, err := os.ReadFile(filepath.Join(out, "fixtures_index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx map[string]struct {
		Type   string          `json:"type"`
		Port   int             `json:"port"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	res := map[string]json.RawMessage{}
	errs := map[string]string{}
	for id, f := range fx {
		doc, err := buildNodeConfigDoc(uuid.New(), f.Type, f.Port, "pandora-native", string(f.Config), nil)
		if err != nil {
			errs[id] = err.Error()
			continue
		}
		res[id] = json.RawMessage(doc.JSON)
	}
	b, err := json.MarshalIndent(res, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	e2eWriteNodeConfig(t, filepath.Join(out, "node_config.json"), b)
	b, err = json.MarshalIndent(errs, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	e2eWriteNodeConfig(t, filepath.Join(out, "node_config_errors.json"), b)
	t.Logf("下发配置 %d 个，被拒 %d 个", len(res), len(errs))
}

func e2eWriteNodeConfig(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
}
