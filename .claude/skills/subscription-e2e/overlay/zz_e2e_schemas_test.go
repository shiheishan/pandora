package nodefabric

// 由 .claude/skills/subscription-e2e/scripts/export-schemas.sh 经 `go test -overlay` 注入，不落仓库。
//
// 把 ProtocolSchemas() 原样导出成前端假后端用的 dev/mock/admin/node-schemas.ts
// （这份文件没有自动同步也没有守卫，改了 schema 要手动重新导出）。

import (
	"encoding/json"
	"os"
	"testing"
)

func TestE2EExportSchemas(t *testing.T) {
	out := os.Getenv("SCHEMA_OUT")
	if out == "" {
		t.Skip("SCHEMA_OUT 未设置：这个测试只由 subscription-e2e 的 export-schemas.sh 调用")
	}
	b, err := json.MarshalIndent(map[string]any{"schemas": ProtocolSchemas()}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	ts := "// prettier-ignore\nexport const NODE_PROTOCOL_SCHEMAS = " + string(b) + " as const\n"
	if err := os.WriteFile(out, []byte(ts), 0o644); err != nil {
		t.Fatal(err)
	}
}
