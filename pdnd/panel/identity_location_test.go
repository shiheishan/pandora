package panel

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func testIdentity(t *testing.T, nodeID string) *Identity {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &Identity{Server: "https://panel.example.test", NodeID: nodeID, Serial: 1,
		PrivateKey: base64.StdEncoding.EncodeToString(private), ConfigKeyID: "old", RuntimeToken: "rt"}
}

// readOnlyDir 建一个本进程写不进去的目录（模拟 ProtectSystem=strict 下的 /etc）。
func readOnlyDir(t *testing.T, identity *Identity) (dir, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root 无视目录权限，模拟不了只读目录")
	}
	dir = filepath.Join(t.TempDir(), "etc")
	path = filepath.Join(dir, "identity.json")
	if err := SaveIdentity(path, identity); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	return dir, path
}

// 身份文件在只读目录：迁到状态目录，旧文件原样保留作回退；迁过去的副本能写
// （配置签名密钥轮换要写回）。原地可写时不迁。
func TestResolveIdentityPathMigratesOutOfReadOnlyDir(t *testing.T) {
	identity := testIdentity(t, "22222222-2222-4222-8222-222222222222")
	_, legacy := readOnlyDir(t, identity)
	state := t.TempDir()
	before, _ := os.ReadFile(legacy)

	got, err := ResolveIdentityPath(legacy, state, "https://panel.example.test", identity.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(IdentityStateDir(state, "https://panel.example.test", identity.NodeID), "identity.json")
	if got != want {
		t.Fatalf("迁移到 %s，期望 %s", got, want)
	}
	if after, _ := os.ReadFile(legacy); string(after) != string(before) {
		t.Fatal("旧位置的身份文件被改动了")
	}
	migrated, err := LoadIdentity(got)
	if err != nil || migrated.PrivateKey != identity.PrivateKey {
		t.Fatalf("迁过去的身份不对：%v", err)
	}
	// 迁过去的那份要可写：模拟一次换钥持久化。
	migrated.ConfigKeyID = "rotated"
	if err := SaveIdentity(got, migrated); err != nil {
		t.Fatalf("迁过去的身份文件写不了：%v", err)
	}

	// 再启动一次：同一身份（只是换过钥），沿用副本，不被旧文件覆盖回去。
	again, err := ResolveIdentityPath(legacy, state, "https://panel.example.test", identity.NodeID)
	if err != nil || again != got {
		t.Fatalf("再次启动没有沿用副本：%s %v", again, err)
	}
	if kept, _ := LoadIdentity(again); kept.ConfigKeyID != "rotated" {
		t.Fatal("换过钥的副本被旧文件覆盖了")
	}

	// 原地可写：不迁。
	writable := filepath.Join(t.TempDir(), "identity.json")
	if err := SaveIdentity(writable, identity); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveIdentityPath(writable, state, "https://panel.example.test", identity.NodeID); err != nil || got != writable {
		t.Fatalf("可写目录里的身份被迁走了：%s %v", got, err)
	}
}

// 旧位置换成了重新接入的新身份（不同私钥）：以旧位置为准覆盖副本。
func TestResolveIdentityPathPrefersReenrolledIdentity(t *testing.T) {
	state := t.TempDir()
	first := testIdentity(t, "22222222-2222-4222-8222-222222222222")
	stale := filepath.Join(IdentityStateDir(state, "https://panel.example.test", first.NodeID), "identity.json")
	if err := SaveIdentity(stale, first); err != nil {
		t.Fatal(err)
	}
	reenrolled := testIdentity(t, first.NodeID)
	reenrolled.Serial = 2
	_, legacy := readOnlyDir(t, reenrolled)
	got, err := ResolveIdentityPath(legacy, state, "https://panel.example.test", first.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded, _ := LoadIdentity(got); loaded.PrivateKey != reenrolled.PrivateKey {
		t.Fatal("重新接入的身份没有覆盖旧副本")
	}
}

func TestIdentityStateDirSeparatesPanelsAndSanitizesNodeID(t *testing.T) {
	a := IdentityStateDir("/var/lib/pandora-native", "https://a.example.test", "7")
	b := IdentityStateDir("/var/lib/pandora-native", "https://b.example.test/", "7")
	if a == b || filepath.Base(a) != "7" {
		t.Fatalf("不同面板的同号节点目录相同或节点段不对：%s %s", a, b)
	}
	if IdentityStateDir("/s", "https://A.example.test/", "7") != IdentityStateDir("/s", "https://a.example.test", "7") {
		t.Fatal("面板地址规整不一致")
	}
	for _, evil := range []string{"../../etc", "a/b", "..", ""} {
		seg := SafePathSegment(evil)
		if seg == evil || filepath.Base(seg) != seg {
			t.Fatalf("不安全的节点 ID %q 被原样当成路径段：%q", evil, seg)
		}
	}
}
