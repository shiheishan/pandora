package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"slices"
	"testing"

	"github.com/aegispanel/nodeagent/panel"
	"github.com/aegispanel/nodeagent/upgrade"
)

func TestBuildSerialValue(t *testing.T) {
	old := buildSerial
	t.Cleanup(func() { buildSerial = old })
	for in, want := range map[string]uint64{"0": 0, "": 0, "dev": 0, "-1": 0, "1792540800": 1792540800, " 42 ": 42} {
		buildSerial = in
		if got := buildSerialValue(); got != want {
			t.Fatalf("buildSerial=%q → %d, want %d", in, got, want)
		}
	}
}

// 本地构建（未注入）必须是 0：dev 构建不声明 upgrade_v1、助手拒绝一切升级。
func TestBuildSerialDefaultsToDev(t *testing.T) {
	if buildSerial != "0" {
		t.Fatalf("buildSerial 缺省值 = %q，应为 \"0\"（由发布构建 -X 注入）", buildSerial)
	}
}

func testKeyring(t *testing.T) *upgrade.Keyring {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return upgrade.NewKeyring(pub)
}

func TestUpgradeUnsupportedReasons(t *testing.T) {
	ok := upgradeEnv{serial: 100, keyring: testKeyring(t), exe: upgrade.DefaultBinPath}
	if why := ok.upgradeUnsupported(); why != "" {
		t.Fatalf("前提齐全时应支持，得 %q", why)
	}
	cases := map[string]func(e *upgradeEnv){
		"dev 构建":     func(e *upgradeEnv) { e.serial = 0 },
		"公钥加载失败":     func(e *upgradeEnv) { e.keyringErr = errors.New("bad pub") },
		"没有内置公钥":     func(e *upgradeEnv) { e.keyring = upgrade.NewKeyring() },
		"取不到二进制路径":   func(e *upgradeEnv) { e.exeErr = errors.New("no exe") },
		"二进制不在固定路径":  func(e *upgradeEnv) { e.exe = "/opt/pandora-native" },
		"没装 path 单元": func(e *upgradeEnv) { e.pathUnitErr = os.ErrNotExist },
	}
	for name, mutate := range cases {
		e := ok
		mutate(&e)
		if why := e.upgradeUnsupported(); why == "" {
			t.Fatalf("%s：应不支持在线升级", name)
		}
	}
}

func TestNewUpgraderAndFeatures(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := loadTestConfig(t, `{"panel":{"address":"panel.example.test"},"upgrade":{"allow_http":true}}`)
	if !cfg.Upgrade.AllowHTTP {
		t.Fatal("upgrade.allow_http 应被解析")
	}

	// 不满足前提：返回接口零值（不是带类型的 nil 指针），Hello 只声明 users_delta。
	up := newUpgrader(cfg, upgradeEnv{}, "sha", log)
	if up != nil {
		t.Fatalf("不支持时应返回 nil，得 %#v", up)
	}
	if got := sessionFeatures(up); !slices.Equal(got, []string{panel.FeatureUsersDelta}) {
		t.Fatalf("features = %v", got)
	}

	env := upgradeEnv{serial: 100, keyring: testKeyring(t), exe: upgrade.DefaultBinPath}
	up = newUpgrader(cfg, env, "abc", log)
	agent, ok := up.(*upgrade.Agent)
	if !ok || agent == nil {
		t.Fatalf("支持时应返回 *upgrade.Agent，得 %#v", up)
	}
	if agent.Dir != upgrade.DefaultStateDir || agent.BuildSerial != 100 || agent.BinarySHA256 != "abc" || !agent.AllowHTTP {
		t.Fatalf("Agent 参数不对: %+v", agent)
	}
	if got := sessionFeatures(up); !slices.Equal(got, []string{panel.FeatureUsersDelta, panel.FeatureUpgrade}) {
		t.Fatalf("features = %v", got)
	}
}

func TestConfigUpgradeRejectsUnknownField(t *testing.T) {
	path := t.TempDir() + "/config.json"
	if err := os.WriteFile(path, []byte(`{"panel":{"address":"p.example.test"},"upgrade":{"url":"https://x.example.test"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(path); err == nil {
		t.Fatal("upgrade 段里的未知字段应报错")
	}
}

func TestUpgradeApplyRejectsArgsAndNonRoot(t *testing.T) {
	if err := upgradeApplyCommand([]string{"/tmp/evil"}, io.Discard); err == nil {
		t.Fatal("upgrade-apply 不应接受参数")
	}
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行测试时跳过非 root 分支")
	}
	if err := upgradeApplyCommand(nil, io.Discard); err == nil {
		t.Fatal("非 root 运行 upgrade-apply 应报错")
	}
}
