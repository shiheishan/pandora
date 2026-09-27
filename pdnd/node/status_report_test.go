package node

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
)

// 默认发布走签名通道。签名心跳不带 metrics 时面板不写 node_metrics，
// 后台资源曲线与节点列表的 CPU / 内存列就一直是空的——节点看着活着，
// 却没有任何资源数据。
func TestSignedStatusReportCarriesHostMetrics(t *testing.T) {
	var got map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/nodes/heartbeat" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("heartbeat body: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"node_status": "active"})
	}))
	defer server.Close()

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := panel.NewSignedClient(&panel.Identity{
		Server: server.URL, NodeID: "node-1", Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(private),
	})
	if err != nil {
		t.Fatal(err)
	}
	client := panel.New(panel.Options{BaseURL: server.URL, NodeID: "n1", NodeType: "vless", Token: "token"})
	n := NewWithSignedClient(client, &rollbackCore{}, testLogger(), signed)

	n.reportStatus(context.Background())

	if got == nil {
		t.Fatal("no signed heartbeat was sent")
	}
	var m map[string]any
	if err := json.Unmarshal(got["metrics"], &m); err != nil || m == nil {
		t.Fatalf("signed heartbeat has no metrics object: %s", got["metrics"])
	}
	for _, key := range []string{"cpu_bp", "mem_used_mb", "mem_total_mb", "disk_used_gb", "disk_total_gb", "load1_cbp", "uptime_sec"} {
		if _, ok := m[key]; !ok {
			t.Errorf("metrics lacks %q", key)
		}
	}
	var cores int
	if err := json.Unmarshal(got["cpu_cores"], &cores); err != nil || cores <= 0 {
		t.Errorf("cpu_cores = %s, want the host core count", got["cpu_cores"])
	}
}
