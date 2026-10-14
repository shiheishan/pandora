package handler

// 由 .agents/skills/subscription-e2e/scripts/export-schemas.sh 经 `go test -overlay` 注入 handler 包，不落仓库。
//
// 把 protocol.Schemas() 经 nodeProtocolSchemaFrom 拼成与 GET /v1/node-protocol-schemas 同形的 JSON，
// 导出到 SCHEMA_OUT（前端假后端 panel/frontend/dev/mock/admin/node-schemas.ts）。
// 这份 mock 没有自动同步也没有守卫，改了 schema 要手动重新导出。

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/aegispanel/aegis/internal/module/node/protocol"
)

func TestE2EExportSchemas(t *testing.T) {
	out := os.Getenv("SCHEMA_OUT")
	if out == "" {
		t.Skip("SCHEMA_OUT 未设置：这个测试只由 subscription-e2e 的 export-schemas.sh 调用")
	}
	list := protocol.Schemas()
	outSchemas := make([]nodeProtocolSchemaResponse, 0, len(list))
	for _, s := range list {
		outSchemas = append(outSchemas, nodeProtocolSchemaFrom(s))
	}
	payload := nodeProtocolSchemasResponse{Schemas: outSchemas}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	ts := "// prettier-ignore\nexport const NODE_PROTOCOL_SCHEMAS = " + string(b) + " as const\n"
	if err := os.WriteFile(out, []byte(ts), 0o644); err != nil {
		t.Fatal(err)
	}
}
