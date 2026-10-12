package nodefabric

// 由 .claude/skills/subscription-e2e/scripts/run.sh 经 `go test -overlay` 注入 nodefabric 包，不落仓库。
//
// 读 subscription 那一步写出的 fixtures_index.json，对每个夹具调用真实的 BuildNodeConfig，
// 得到面板下发给节点的配置（E2E 服务端用它起入站），写到 E2E_OUT/node_config.json。
// BuildNodeConfig 拒绝的夹具（如 cipher 与 method 冲突）记进 node_config_errors.json，不判失败。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
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
	s := &Service{}
	res := map[string]json.RawMessage{}
	errs := map[string]string{}
	for id, f := range fx {
		body, _, err := s.BuildNodeConfig(&ServingNode{Name: id, NodeType: f.Type, ServerHost: "node.example.com", ServerPort: f.Port, Protocol: f.Config, Kernel: "pandora-native"})
		if err != nil {
			errs[id] = err.Error()
			continue
		}
		res[id] = body
	}
	b, _ := json.MarshalIndent(res, "", " ")
	if err := os.WriteFile(filepath.Join(out, "node_config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ = json.MarshalIndent(errs, "", " ")
	if err := os.WriteFile(filepath.Join(out, "node_config_errors.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("下发配置 %d 个，被拒 %d 个", len(res), len(errs))
}
