package outbound

import (
	"encoding/json"
	"strings"
	"testing"
)

// 签名通道（UseNumber）下发的出站，server_port 是 json.Number；以前 Int 只认
// float64，端口被读成 0，出站整个被判非法。
func TestServerAcceptsSignedJSONNumberPort(t *testing.T) {
	var settings map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"server":"relay.example","server_port":8443,"tls":1}`))
	decoder.UseNumber()
	if err := decoder.Decode(&settings); err != nil {
		t.Fatal(err)
	}
	addr, err := Server(settings)
	if err != nil {
		t.Fatalf("Server: %v", err)
	}
	if addr.Port != 8443 {
		t.Fatalf("port = %d, want 8443", addr.Port)
	}
	if !Bool(settings, "tls") {
		t.Fatal("数值形态的开关被读成 false")
	}
}
