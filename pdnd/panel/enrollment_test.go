package panel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// 两阶段接入是节点首装唯一的入口（一步式 /v1/nodes/bootstrap 面板已返回
// 426）。走完 begin → commit，身份文件里必须有签名通道要用的全部字段，
// 运行令牌要和 begin 时报给面板的摘要对得上——对不上的话身份文件看着完整，
// 第一次签名请求就会被面板拒掉。
func TestEnrollmentBeginCommitPersistsCompleteIdentity(t *testing.T) {
	configPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeHash string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/enrollments":
			raw, _ := io.ReadAll(r.Body)
			var begin struct {
				RequestID    string `json:"request_id"`
				PublicKey    string `json:"public_key"`
				RuntimeToken string `json:"runtime_token_sha256"`
			}
			if err := json.Unmarshal(raw, &begin); err != nil {
				t.Fatalf("begin body: %v", err)
			}
			pub, _ := base64.StdEncoding.DecodeString(begin.PublicKey)
			sig, _ := base64.StdEncoding.DecodeString(r.Header.Get("X-Enrollment-Signature"))
			hash := sha256.Sum256(raw)
			payload := enrollmentBeginDomainV1 + "\nPOST\n/v1/nodes/enrollments\n" + begin.RequestID + "\n" + base64.StdEncoding.EncodeToString(hash[:])
			if !ed25519.Verify(pub, []byte(payload), sig) {
				t.Fatal("begin signature does not verify against the submitted public key")
			}
			runtimeHash = begin.RuntimeToken
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"enrollment_id": "enr-1", "node_id": "node-1", "serial": 1, "state": "pending",
				"config_key_id": "key-1", "config_public_key": base64.StdEncoding.EncodeToString(configPublic),
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/nodes/enrollments/enr-1/commit":
			if r.Header.Get("X-Node-Id") != "node-1" || r.Header.Get("X-Node-Sig") == "" {
				t.Fatalf("commit is not signed with the pending identity: %v", r.Header)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "committed"})
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "identity.json")
	j, err := BeginEnrollment(context.Background(), BootstrapOptions{
		Server: server.URL, Token: "fixture-bootstrap-token", Name: "node-1", Path: path,
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.BootstrapToken != "" {
		t.Fatal("journal still holds the one-time bootstrap token after begin succeeded")
	}
	if _, err := CommitEnrollment(context.Background(), path, j, EnrollmentEvidence{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.NodeID != "node-1" || loaded.Serial != 1 || loaded.ConfigKeyID != "key-1" ||
		loaded.ConfigPublicKey == "" || loaded.PrivateKey == "" || loaded.RuntimeToken == "" {
		t.Fatalf("persisted identity is incomplete: %+v", loaded)
	}
	sum := sha256.Sum256([]byte(loaded.RuntimeToken))
	if base64.StdEncoding.EncodeToString(sum[:]) != runtimeHash {
		t.Fatal("persisted runtime token does not match the digest sent at begin")
	}
}

// 面板回的 begin 响应缺字段时必须失败，且不能留下身份文件：半截身份会让
// 节点以为自己已接入，之后每个签名请求都失败，安装器也不会再重试接入。
func TestEnrollmentBeginRejectsIncompleteResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"enrollment_id":"enr-1","node_id":"node-1","serial":1}`))
	}))
	defer server.Close()

	path := filepath.Join(t.TempDir(), "identity.json")
	_, err := BeginEnrollment(context.Background(), BootstrapOptions{
		Server: server.URL, Token: "fixture-bootstrap-token", Name: "node-1", Path: path,
	})
	if err == nil {
		t.Fatal("incomplete enrollment response was accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("identity file exists after a rejected begin: %v", err)
	}
}

// begin 请求要带上本机容量：面板在接入事务里把它写进 nodes，节点从出现在
// 后台那一刻起就有 CPU / 内存 / 磁盘，而不是一直空到第一次签名心跳。
func TestEnrollmentBeginReportsHostCapacity(t *testing.T) {
	saved := collectEnrollmentCapacity
	collectEnrollmentCapacity = func() HostCapacity { return HostCapacity{CPUCores: 4, MemoryMB: 8192, DiskGB: 80} }
	defer func() { collectEnrollmentCapacity = saved }()

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("begin body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"enrollment_id": "enr-1", "node_id": "node-1", "serial": 1, "state": "pending",
			"config_key_id": "key-1", "config_public_key": "fixture-config-public-key",
		})
	}))
	defer server.Close()

	if _, err := BeginEnrollment(context.Background(), BootstrapOptions{
		Server: server.URL, Token: "fixture-bootstrap-token", Name: "node-1", Path: filepath.Join(t.TempDir(), "identity.json"),
	}); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"cpu_cores": 4, "memory_mb": 8192, "disk_gb": 80} {
		if got[key] != want {
			t.Errorf("begin %s = %v, want %v", key, got[key], want)
		}
	}
}
