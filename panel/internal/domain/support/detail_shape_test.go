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
