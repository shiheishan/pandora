package route

import (
	"encoding/json"
	"strings"
	"testing"
)

// 签名通道（UseNumber）下发的路由规则，端口是 json.Number；以前 toStrings
// 只认 float64 / int，整条规则编译失败。
func TestToStringsAcceptsSignedJSONNumber(t *testing.T) {
	var matcher map[string]any
	decoder := json.NewDecoder(strings.NewReader(`{"port":[443,8443],"source_port":53}`))
	decoder.UseNumber()
	if err := decoder.Decode(&matcher); err != nil {
		t.Fatal(err)
	}
	ports, err := toStrings(matcher["port"])
	if err != nil || strings.Join(ports, ",") != "443,8443" {
		t.Fatalf("port = %v, %v", ports, err)
	}
	source, err := toStrings(matcher["source_port"])
	if err != nil || strings.Join(source, ",") != "53" {
		t.Fatalf("source_port = %v, %v", source, err)
	}
	if _, err := toStrings(json.Number("1.5")); err == nil {
		t.Fatal("非整数端口被接受")
	}
}
