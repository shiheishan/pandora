// [INPUT]: 依赖 service.go 的 Ticket 与 TicketDetail、encoding/json
// [OUTPUT]: 对外提供 TestTicketDetailAlwaysCarriesMessagesArray
// [POS]: domain/support 的响应形状单测：详情的 messages 总是数组（空时 []），列表行不带 messages 键
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package support

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTicketDetailAlwaysCarriesMessagesArray(t *testing.T) {
	// GetForAgent / GetForUser 都先把 Messages 置成 []Message{} 再追加
	raw, err := json.Marshal(TicketDetail{Messages: []Message{}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"messages":[]`) {
		t.Errorf("detail JSON %s lacks \"messages\":[]", raw)
	}
	raw, err = json.Marshal(Ticket{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"messages"`) {
		t.Errorf("list row JSON carries messages: %s", raw)
	}
}
