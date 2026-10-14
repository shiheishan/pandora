package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
	xraycore "github.com/xtls/xray-core/core"
)

func testProfile(t *testing.T) (*profile.Profile, profile.User) {
	dir := t.TempDir()
	p, err := profile.Init(profile.InitOptions{
		Dir:         dir,
		Seed:        "nodegate-test",
		Users:       20,
		DeltaSize:   5,
		RealityDest: "127.0.0.1:19443",
	})
	if err != nil {
		t.Fatal(err)
	}
	user := profile.User{Index: 0, UID: 1, UUID: profile.UserUUID(p.Seed, 0)}
	return p, user
}

func assertXrayConfigParses(t *testing.T, cfg []byte) {
	t.Helper()
	if _, err := xraycore.LoadConfig("json", bytes.NewReader(cfg)); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
}

func outboundMap(t *testing.T, cfg []byte) map[string]any {
	t.Helper()
	var root map[string]any
	if err := json.Unmarshal(cfg, &root); err != nil {
		t.Fatal(err)
	}
	outs, _ := root["outbounds"].([]any)
	if len(outs) == 0 {
		t.Fatal("无 outbounds")
	}
	ob, _ := outs[0].(map[string]any)
	return ob
}

func streamMap(ob map[string]any) map[string]any {
	ss, _ := ob["streamSettings"].(map[string]any)
	return ss
}

func TestXrayClientConfigVLESSVision(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.VLESSVision, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ob := outboundMap(t, cfg)
	if ob["protocol"] != "vless" {
		t.Fatalf("protocol=%v", ob["protocol"])
	}
	ss := streamMap(ob)
	if ss["network"] != "tcp" || ss["security"] != "reality" {
		t.Fatalf("stream=%v", ss)
	}
	real, _ := ss["realitySettings"].(map[string]any)
	if real["publicKey"] != p.Reality.PublicKey || real["shortId"] != p.Reality.ShortID || real["serverName"] != p.Reality.ServerName {
		t.Fatalf("reality=%v", real)
	}
	settings, _ := ob["settings"].(map[string]any)
	vnext, _ := settings["vnext"].([]any)
	vn, _ := vnext[0].(map[string]any)
	if vn["port"] != float64(p.Port(profile.VLESSVision)) {
		t.Fatalf("port=%v", vn["port"])
	}
	users, _ := vn["users"].([]any)
	u0, _ := users[0].(map[string]any)
	if u0["id"] != user.UUID || u0["flow"] != "xtls-rprx-vision" {
		t.Fatalf("user=%v", u0)
	}
}

func TestXrayClientConfigVLESSGRPC(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.VLESSGRPC, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ob := outboundMap(t, cfg)
	ss := streamMap(ob)
	if ss["network"] != "grpc" {
		t.Fatalf("network=%v", ss["network"])
	}
	grpc, _ := ss["grpcSettings"].(map[string]any)
	if grpc["serviceName"] != profile.GRPCService {
		t.Fatalf("grpc=%v", grpc)
	}
	settings, _ := ob["settings"].(map[string]any)
	vnext, _ := settings["vnext"].([]any)
	vn, _ := vnext[0].(map[string]any)
	users, _ := vn["users"].([]any)
	u0, _ := users[0].(map[string]any)
	if _, has := u0["flow"]; has {
		t.Fatalf("grpc 用户不应带 flow: %v", u0)
	}
}

func TestXrayClientConfigTrojanTCP(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.TrojanTCP, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ob := outboundMap(t, cfg)
	if ob["protocol"] != "trojan" {
		t.Fatal(ob["protocol"])
	}
	ss := streamMap(ob)
	if ss["network"] != "tcp" || ss["security"] != "tls" {
		t.Fatalf("stream=%v", ss)
	}
	tls, _ := ss["tlsSettings"].(map[string]any)
	if tls["serverName"] != profile.ServerName || tls["pinnedPeerCertSha256"] != p.Cert.LeafSHA256 {
		t.Fatalf("tls=%v", tls)
	}
	settings, _ := ob["settings"].(map[string]any)
	servers, _ := settings["servers"].([]any)
	s0, _ := servers[0].(map[string]any)
	if s0["password"] != user.UUID {
		t.Fatalf("password=%v", s0["password"])
	}
}

func TestXrayClientConfigTrojanWS(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.TrojanWS, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ss := streamMap(outboundMap(t, cfg))
	if ss["network"] != "ws" {
		t.Fatal(ss["network"])
	}
	ws, _ := ss["wsSettings"].(map[string]any)
	if ws["path"] != profile.TrojanWSPath || ws["host"] != profile.ServerName {
		t.Fatalf("ws=%v", ws)
	}
}

func TestXrayClientConfigSS(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.SS, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ob := outboundMap(t, cfg)
	if ob["protocol"] != "shadowsocks" {
		t.Fatal(ob["protocol"])
	}
	settings, _ := ob["settings"].(map[string]any)
	servers, _ := settings["servers"].([]any)
	s0, _ := servers[0].(map[string]any)
	if s0["method"] != profile.SSMethod || s0["password"] != user.UUID {
		t.Fatalf("ss=%v", s0)
	}
}

func TestXrayClientConfigVMessWS(t *testing.T) {
	p, user := testProfile(t)
	cfg, err := xrayClientConfig(p, profile.VMessWS, "10.0.0.1", user)
	if err != nil {
		t.Fatal(err)
	}
	assertXrayConfigParses(t, cfg)
	ob := outboundMap(t, cfg)
	if ob["protocol"] != "vmess" {
		t.Fatal(ob["protocol"])
	}
	ss := streamMap(ob)
	ws, _ := ss["wsSettings"].(map[string]any)
	if ws["path"] != profile.VMessWSPath || ws["host"] != profile.ServerName {
		t.Fatalf("ws=%v", ws)
	}
	settings, _ := ob["settings"].(map[string]any)
	vnext, _ := settings["vnext"].([]any)
	vn, _ := vnext[0].(map[string]any)
	users, _ := vn["users"].([]any)
	u0, _ := users[0].(map[string]any)
	if u0["alterId"] != float64(0) || u0["security"] != "auto" {
		t.Fatalf("user=%v", u0)
	}
}

func TestPickUsers(t *testing.T) {
	p := &profile.Profile{Seed: "nodegate", Users: 5000, DeltaSize: 500}
	users := pickUsers(p, 16)
	if len(users) != 16 {
		t.Fatalf("len=%d", len(users))
	}
	seen := make(map[int]bool)
	for _, u := range users {
		if u.Index >= 4500 {
			t.Fatalf("下标 %d 应 < 4500", u.Index)
		}
		if seen[u.Index] {
			t.Fatalf("重复下标 %d", u.Index)
		}
		seen[u.Index] = true
	}
	if users[0].Index != 0 {
		t.Fatalf("第 0 个下标=%d", users[0].Index)
	}
}

func TestClientTLSCloneIndependent(t *testing.T) {
	dir := t.TempDir()
	p, err := profile.Init(profile.InitOptions{
		Dir: dir, Users: 20, DeltaSize: 5, RealityDest: "127.0.0.1:19443",
	})
	if err != nil {
		t.Fatal(err)
	}
	orig, err := newClientTLS(p)
	if err != nil {
		t.Fatal(err)
	}
	cloned := orig.Clone().(*clientTLS)
	cloned.SetServerName("other.example.test")
	if orig.ServerName() != profile.ServerName {
		t.Fatalf("原 ServerName 被改: %q", orig.ServerName())
	}
	if cloned.ServerName() != "other.example.test" {
		t.Fatalf("clone ServerName=%q", cloned.ServerName())
	}
}

// deadlineConn：期限到了读返回 os.ErrDeadlineExceeded；期限清掉后连接不被关。
func TestDeadlineConn(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := newDeadlineConn(a)
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("期限到期应返回 ErrDeadlineExceeded，得到 %v", err)
	}

	x, y := net.Pipe()
	defer y.Close()
	c2 := newDeadlineConn(x)
	defer c2.Close()
	_ = c2.SetDeadline(time.Now().Add(50 * time.Millisecond))
	_ = c2.SetDeadline(time.Time{})
	time.Sleep(100 * time.Millisecond)
	go func() { _, _ = y.Write([]byte{1}) }()
	if _, err := c2.Read(make([]byte, 1)); err != nil {
		t.Fatalf("清掉期限后连接应仍可用: %v", err)
	}
}
