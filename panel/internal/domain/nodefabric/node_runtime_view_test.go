package nodefabric

import (
	"os"
	"strings"
	"testing"
)

// 全新安装一定建成唯一索引，列表不再为存量端口冲突逐行做 LATERAL。
func TestNodeListOmitsLegacyPortConflict(t *testing.T) {
	raw, err := os.ReadFile("node_runtime_view.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if strings.Contains(src, "port_conflict_node") || strings.Contains(src, "PortConflictNode") {
		t.Fatal("node list still carries the legacy port-conflict column")
	}
}
