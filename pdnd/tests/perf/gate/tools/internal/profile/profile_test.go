package profile

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUserUUIDDeterministicAndFormat(t *testing.T) {
	u1 := UserUUID("nodegate", 0)
	u2 := UserUUID("nodegate", 0)
	if u1 != u2 {
		t.Fatalf("同输入应相同: %q vs %q", u1, u2)
	}
	u3 := UserUUID("nodegate", 1)
	if u1 == u3 {
		t.Fatalf("不同 i 应不同")
	}
	if !uuidV4Re.MatchString(u1) {
		t.Fatalf("格式不对: %s", u1)
	}
}

func TestParseProtocols(t *testing.T) {
	all, err := ParseProtocols("all")
	if err != nil || !reflect.DeepEqual(all, All) {
		t.Fatalf("all: %v %v", all, err)
	}
	sub, err := ParseProtocols("hy2,trojan_tcp,vless_vision")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{VLESSVision, TrojanTCP, Hy2}
	if !reflect.DeepEqual(sub, want) {
		t.Fatalf("子集顺序: got %v want %v", sub, want)
	}
	if _, err := ParseProtocols("nope"); err == nil {
		t.Fatal("未知名应报错")
	}
	dedup, err := ParseProtocols("ss,ss,trojan_tcp")
	if err != nil {
		t.Fatal(err)
	}
	if len(dedup) != 2 || dedup[0] != TrojanTCP || dedup[1] != SS {
		t.Fatalf("去重: %v", dedup)
	}
}

func TestRosterAndDelta(t *testing.T) {
	p := &Profile{Seed: "nodegate", Users: 5000, DeltaSize: 500}
	r0 := p.Roster(0)
	if len(r0) != 5000 {
		t.Fatalf("Roster(0) 长度 %d", len(r0))
	}
	for i, u := range r0 {
		if u.Index != i {
			t.Fatalf("下标 %d", i)
		}
	}
	if r0[0].Index != 0 || r0[len(r0)-1].Index != 4999 {
		t.Fatalf("Roster(0) 应为 [0,5000)")
	}

	r1 := p.Roster(1)
	if len(r1) != 5000 {
		t.Fatalf("Roster(1) 长度 %d", len(r1))
	}
	inR1 := make(map[int]bool, len(r1))
	for _, u := range r1 {
		inR1[u.Index] = true
	}
	// 稳定核 [0, N-K) 全部保留
	for i := 0; i < 4500; i++ {
		if !inR1[i] {
			t.Fatalf("稳定核缺 %d", i)
		}
	}
	for _, u := range r1 {
		if u.Index >= 4500 && u.Index < 5000 {
			t.Fatalf("Roster(1) 不应含 block(0) 下标 %d", u.Index)
		}
	}
	for i := 5000; i < 5500; i++ {
		if !inR1[i] {
			t.Fatalf("Roster(1) 缺 block(1) %d", i)
		}
	}

	ups, rem := p.Delta(1)
	if len(ups) != 500 {
		t.Fatalf("upserted %d", len(ups))
	}
	if ups[0].Index != 5000 || ups[len(ups)-1].Index != 5499 {
		t.Fatalf("upserted 范围")
	}
	if len(rem) != 500 || rem[0] != 4501 || rem[len(rem)-1] != 5000 {
		t.Fatalf("removed %v", rem)
	}

	// Delta 与 Roster 差一致
	oldSet := make(map[int64]bool)
	for _, u := range r0 {
		oldSet[u.UID] = true
	}
	newSet := make(map[int64]bool)
	for _, u := range r1 {
		newSet[u.UID] = true
	}
	for _, u := range ups {
		if !newSet[u.UID] {
			t.Fatalf("upserted 不在新名单")
		}
	}
	for _, uid := range rem {
		if oldSet[uid] == false {
			t.Fatalf("removed 不在旧名单 %d", uid)
		}
		if newSet[uid] {
			t.Fatalf("removed 仍在新名单 %d", uid)
		}
	}
}

func TestInitLoadAndCerts(t *testing.T) {
	dir := t.TempDir()
	orig, err := Init(InitOptions{
		Dir:         dir,
		RealityDest: "127.0.0.1:19443",
		Users:       100,
		DeltaSize:   10,
		PortBase:    20000,
		Protocols:   []string{SS, Hy2},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(filepath.Join(dir, "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != orig.Version || loaded.Users != 100 || loaded.DeltaSize != 10 {
		t.Fatalf("Load 字段: %+v", loaded)
	}
	for _, name := range []string{"certs/ca.crt", "certs/node.crt", "certs/node.key"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(name, err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "certs/node.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("node.key 权限 %o", info.Mode().Perm())
	}

	crtPEM, _ := os.ReadFile(filepath.Join(dir, "certs/node.crt"))
	block, _ := pem.Decode(crtPEM)
	if block == nil {
		t.Fatal("无叶子 PEM")
	}
	leafSum := sha256.Sum256(block.Bytes)
	if hex.EncodeToString(leafSum[:]) != loaded.Cert.LeafSHA256 {
		t.Fatalf("LeafSHA256 不一致")
	}

	caPEM, _ := os.ReadFile(filepath.Join(dir, "certs/ca.crt"))
	caPool := x509.NewCertPool()
	caPool.AppendCertsFromPEM(caPEM)
	leafCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leafCert.Verify(x509.VerifyOptions{DNSName: ServerName, Roots: caPool}); err != nil {
		t.Fatal(err)
	}
	if err := leafCert.VerifyHostname(ServerName); err != nil {
		t.Fatal(err)
	}
}

func expectedConfigKeys(proto string) map[string]bool {
	common := map[string]bool{"server_port": true, "protocol": true}
	switch proto {
	case VLESSVision:
		return mergeKeys(common, "network", "security", "flow", "dest", "server_names", "private_key", "public_key", "short_ids", "fingerprint")
	case VLESSGRPC:
		return mergeKeys(common, "network", "grpc_service_name", "security", "dest", "server_names", "private_key", "public_key", "short_ids", "fingerprint")
	case TrojanTCP:
		return mergeKeys(common, "network", "security", "tls", "cert_path", "key_path", "server_name")
	case TrojanWS:
		return mergeKeys(common, "network", "path", "host", "security", "tls", "cert_path", "key_path", "server_name")
	case SS:
		return mergeKeys(common, "method", "network")
	case VMessWS:
		return mergeKeys(common, "network", "path", "host", "security")
	case Hy2:
		return mergeKeys(common, "cert_path", "key_path", "server_name")
	case TUIC:
		return mergeKeys(common, "cert_path", "key_path", "congestion_control", "server_name")
	case AnyTLS:
		return mergeKeys(common, "cert_path", "key_path", "server_name")
	default:
		return common
	}
}

func mergeKeys(base map[string]bool, keys ...string) map[string]bool {
	out := make(map[string]bool)
	for k, v := range base {
		out[k] = v
	}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func TestConfigDocPerProtocol(t *testing.T) {
	dir := t.TempDir()
	p, err := Init(InitOptions{Dir: dir, RealityDest: "127.0.0.1:19443"})
	if err != nil {
		t.Fatal(err)
	}
	for _, proto := range All {
		body1, sha1, err := p.ConfigDoc(proto)
		if err != nil {
			t.Fatalf("%s: %v", proto, err)
		}
		body2, sha2, err := p.ConfigDoc(proto)
		if err != nil || !reflect.DeepEqual(body1, body2) || sha1 != sha2 {
			t.Fatalf("%s 两次不一致", proto)
		}
		sum := sha256.Sum256(body1)
		if hex.EncodeToString(sum[:]) != sha1 {
			t.Fatalf("%s sha 不对", proto)
		}
		var obj map[string]any
		if err := json.Unmarshal(body1, &obj); err != nil {
			t.Fatal(err)
		}
		if int(obj["server_port"].(float64)) != p.Port(proto) {
			t.Fatalf("%s server_port", proto)
		}
		if obj["protocol"] != NodeType(proto) {
			t.Fatalf("%s protocol", proto)
		}
		wantKeys := expectedConfigKeys(proto)
		if len(obj) != len(wantKeys) {
			t.Fatalf("%s 键数 got %d want %d keys %v", proto, len(obj), len(wantKeys), obj)
		}
		for k := range wantKeys {
			if _, ok := obj[k]; !ok {
				t.Fatalf("%s 缺键 %s", proto, k)
			}
		}
	}

	priv, err := base64.RawURLEncoding.DecodeString(p.Reality.PrivateKey)
	if err != nil || len(priv) != 32 {
		t.Fatalf("private_key: %v len %d", err, len(priv))
	}
	if len(p.Reality.ShortID) != 16 {
		t.Fatalf("short_id hex 长度 %d", len(p.Reality.ShortID))
	}
	if _, err := hex.DecodeString(p.Reality.ShortID); err != nil {
		t.Fatal(err)
	}
	body, _, _ := p.ConfigDoc(VLESSVision)
	var doc map[string]any
	json.Unmarshal(body, &doc)
	sids, _ := doc["short_ids"].([]interface{})
	if len(sids) != 1 || sids[0] != p.Reality.ShortID {
		t.Fatalf("short_ids %v", sids)
	}
}
