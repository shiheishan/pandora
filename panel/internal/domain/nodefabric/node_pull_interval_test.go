package nodefabric

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNodePullIntervalDefaultsAndBounds(t *testing.T) {
	svc := NewService(nil, nil)
	body, _, err := svc.BuildNodeConfig(&ServingNode{Name: "n", NodeType: "vless", ServerPort: 443})
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Base struct {
			Pull int `json:"pull_interval"`
		} `json:"base_config"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil || cfg.Base.Pull != 15 {
		t.Fatalf("default pull_interval = %d, %v; want 15", cfg.Base.Pull, err)
	}
	for _, bad := range []time.Duration{0, 4 * time.Second, 301 * time.Second, 1500 * time.Millisecond} {
		if err := svc.SetNodePullInterval(bad); err == nil {
			t.Fatalf("accepted pull interval %s", bad)
		}
	}
	if err := svc.SetNodePullInterval(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	body, _, _ = svc.BuildNodeConfig(&ServingNode{Name: "n", NodeType: "vless", ServerPort: 443})
	if err := json.Unmarshal(body, &cfg); err != nil || cfg.Base.Pull != 30 {
		t.Fatalf("configured pull_interval = %d, %v; want 30", cfg.Base.Pull, err)
	}
}
