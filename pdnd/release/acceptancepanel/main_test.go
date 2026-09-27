package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	nodepanel "github.com/aegispanel/nodeagent/panel"
)

// 夹具是按面板实现独立重写的；这里用节点自己的签名客户端对打，保证它签出的
// effective release 能被节点验过、节点的签名请求能被它验过。任何一边的协议
// 改了而另一边没跟上，CI 在这里变红，而不是等到有人在验收机上跑脚本。
func TestSignedChannelRoundTripWithNodeClient(t *testing.T) {
	p := &panel{nodePort: 18443, statePath: filepath.Join(t.TempDir(), "state.json"), state: State{ReportPhases: []string{}}}
	server := httptest.NewServer(p)
	defer server.Close()
	id, err := p.enableSigned("6f1c1f0e-2b7a-4c55-9a3e-1d2c3b4a5f60", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := nodepanel.NewSignedClient(&nodepanel.Identity{
		Server: id.Server, NodeID: id.NodeID, Serial: id.Serial, PrivateKey: id.PrivateKey,
		ConfigPublicKey: id.ConfigPublicKey, ConfigKeyID: id.ConfigKeyID, RuntimeToken: id.RuntimeToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cfg, err := client.Config(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.VerifyConfig(cfg); err != nil {
		t.Fatalf("node rejects the fixture's effective release: %v", err)
	}
	if err := client.ReportEffectiveConfig(ctx, cfg, "switched", ""); err != nil {
		t.Fatal(err)
	}
	in := nodepanel.HeartbeatInput{AgentVersion: "test", RuntimeStatus: "running"}
	in.AttachHostMetrics()
	if _, err := client.Heartbeat(ctx, in); err != nil {
		t.Fatal(err)
	}

	s := p.state
	if s.Mode != "signed" || s.EffectiveConfigFetches != 1 || len(s.ReportPhases) != 1 || s.ReportPhases[0] != "switched" ||
		s.Heartbeats != 1 || s.HeartbeatsWithMetrics != 1 || s.BadSignatures != 0 || s.UniProxyConfigHits != 0 {
		t.Fatalf("unexpected observations: %+v", s)
	}
}

// 签名不对的请求必须被拒并计数，否则脚本的 bad_signatures == 0 断言形同虚设。
func TestSignedChannelRejectsForgedRequest(t *testing.T) {
	p := &panel{nodePort: 18443, statePath: filepath.Join(t.TempDir(), "state.json"), state: State{ReportPhases: []string{}}}
	server := httptest.NewServer(p)
	defer server.Close()
	if _, err := p.enableSigned("6f1c1f0e-2b7a-4c55-9a3e-1d2c3b4a5f60", server.URL); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/nodes/heartbeat", nil)
	req.Header.Set("X-Node-Id", p.nodeID)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(make([]byte, 64)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || p.state.BadSignatures != 1 || p.state.Heartbeats != 0 {
		t.Fatalf("forged request: status %d, state %+v", resp.StatusCode, p.state)
	}
}

func TestEnableSignedRequiresCanonicalNodeID(t *testing.T) {
	p := &panel{}
	for _, id := range []string{"", "runtime-acceptance", "6F1C1F0E-2B7A-4C55-9A3E-1D2C3B4A5F60", "00000000-0000-0000-0000-000000000000"} {
		if _, err := p.enableSigned(id, "http://127.0.0.1:1"); err == nil {
			t.Errorf("node id %q accepted", id)
		}
	}
}
