package service

// 安装脚本的测试：语法（bash -n）、契约片段、参数校验（PANDORA_INSTALL_DRY_RUN）、
// 以及用 PANDORA_INSTALL_LIB_ONLY 只 source 函数、单独跑配置改写与系统参数两段（不要 root、不碰本机）。

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const (
	validPanel = "node.example.com:9443"
	validToken = "tok_ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
)

var validCA = "sha256:" + strings.Repeat("ab", 32)

// bashOrSkip 返回 bash 路径；没有 bash 或版本低于 4 时跳过。
func bashOrSkip(t *testing.T) string {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("没有 bash")
	}
	out, err := exec.Command(bash, "-c", "echo ${BASH_VERSINFO[0]}").Output()
	if err != nil {
		t.Skipf("读不到 bash 版本: %v", err)
	}
	if major, err := strconv.Atoi(strings.TrimSpace(string(out))); err != nil || major < 4 {
		t.Skip("需要 bash 4+")
	}
	return bash
}

// writeScript 把脚本写进临时目录，返回路径。
func writeScript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeRenderedScript 写出按 https://panel.example.com 渲染的脚本。
func writeRenderedScript(t *testing.T) string {
	t.Helper()
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(t.TempDir(), "https://panel.example.com")
	body, err := s.PdndInstallScript()
	if err != nil {
		t.Fatal(err)
	}
	return writeScript(t, body)
}

// runScript 用 bash 跑脚本，返回合并输出与退出码。
func runScript(t *testing.T, bash, script string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bash, append([]string{script}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("跑脚本失败: %v\n%s", err, out)
	return "", -1
}

// runLib 以 PANDORA_INSTALL_LIB_ONLY=1 source 脚本后执行 snippet（$0 是脚本路径，其后是 args）。
func runLib(t *testing.T, bash, script, snippet string, env []string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bash, append([]string{"-c", `. "$0"; ` + snippet, script}, args...)...)
	cmd.Env = append(os.Environ(), append([]string{"PANDORA_INSTALL_LIB_ONLY=1"}, env...)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	}
	t.Fatalf("跑脚本函数失败: %v\n%s", err, out)
	return "", -1
}

func TestInstallScriptSyntax(t *testing.T) {
	bash := bashOrSkip(t)
	for name, path := range map[string]string{
		"rendered": writeRenderedScript(t),
		"template": writeScript(t, pdndInstallTemplate),
	} {
		if out, err := exec.Command(bash, "-n", path).CombinedOutput(); err != nil {
			t.Fatalf("%s: bash -n 失败: %v\n%s", name, err, out)
		}
	}
}

func TestInstallScriptContract(t *testing.T) {
	for _, want := range []string{
		"set -euo pipefail",
		"--token-stdin",
		"unset TOKEN",
		"validate-install",
		"pandora_tune_sysctl || echo",
		`systemctl enable --now "${UPGRADE_UNIT}.path"`,
	} {
		if !strings.Contains(pdndInstallTemplate, want) {
			t.Errorf("脚本缺少 %q", want)
		}
	}
	for _, banned := range []string{"enrollment begin", "enrollment commit", "--token-file", "identity.json"} {
		if strings.Contains(pdndInstallTemplate, banned) {
			t.Errorf("脚本不应出现 %q", banned)
		}
	}
	lines := strings.Split(strings.TrimRight(pdndInstallTemplate, "\n"), "\n")
	last := lines[len(lines)-1]
	// 整段包在函数里、最后一行才调用：curl | bash 时子进程读不到脚本剩下的内容
	if last != `if [[ "${PANDORA_INSTALL_LIB_ONLY:-}" != 1 ]]; then main "$@" </dev/null; fi` {
		t.Fatalf("最后一行应只调用 main：%q", last)
	}
}

func TestInstallScriptDryRunArgs(t *testing.T) {
	bash := bashOrSkip(t)
	script := writeRenderedScript(t)
	upperCA := "sha256:" + strings.Repeat("AB", 32)
	full := func(panel, ca, token string) []string {
		return []string{"--panel", panel, "--ca-sha256", ca, "--token", token}
	}
	cases := []struct {
		name     string
		args     []string
		env      []string
		wantCode int
		wantOut  []string
	}{
		{"合法", full(validPanel, validCA, validToken), nil, 0,
			[]string{"panel=" + validPanel, "origin=https://panel.example.com", "ca=" + validCA}},
		{"CA 大写转小写", full(validPanel, upperCA, validToken), nil, 0, []string{"ca=" + validCA}},
		{"IPv6", full("[2001:db8::1]:9443", validCA, validToken), nil, 0, []string{"panel=[2001:db8::1]:9443"}},
		{"缺 token", []string{"--panel", validPanel, "--ca-sha256", validCA}, nil, 2, nil},
		{"缺 panel", []string{"--ca-sha256", validCA, "--token", validToken}, nil, 2, nil},
		{"panel 没给值", []string{"--ca-sha256", validCA, "--token", validToken, "--panel"}, nil, 2, nil},
		{"未知参数", append(full(validPanel, validCA, validToken), "--name", "x"), nil, 2, nil},
		{"无端口", full("node.example.com", validCA, validToken), nil, 2, nil},
		{"端口 0", full("node.example.com:0", validCA, validToken), nil, 2, nil},
		{"端口越界", full("node.example.com:70000", validCA, validToken), nil, 2, nil},
		{"带协议", full("https://node.example.com:9443", validCA, validToken), nil, 2, nil},
		{"指纹太短", full(validPanel, "sha256:1234", validToken), nil, 2, nil},
		{"指纹算法不对", full(validPanel, "md5:"+strings.Repeat("a", 64), validToken), nil, 2, nil},
		{"令牌带 shell 字符", full(validPanel, validCA, "abc$(id)xyzxyzxyzxyz"), nil, 2, nil},
		{"令牌太短", full(validPanel, validCA, "short"), nil, 2, nil},
		{"https 覆盖", full(validPanel, validCA, validToken), []string{"PANDORA_PANEL_URL=https://mirror.example.com/"}, 0,
			[]string{"origin=https://mirror.example.com"}},
		{"非本机 http 覆盖", full(validPanel, validCA, validToken), []string{"PANDORA_PANEL_URL=http://panel.example.com"}, 2, nil},
		{"本机 http 覆盖", full(validPanel, validCA, validToken), []string{"PANDORA_PANEL_URL=http://127.0.0.1:9000"}, 0,
			[]string{"origin=http://127.0.0.1:9000"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := append([]string{"PANDORA_INSTALL_DRY_RUN=1", "PANDORA_PANEL_URL="}, tc.env...)
			out, code := runScript(t, bash, script, env, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("退出码 %d，期望 %d：\n%s", code, tc.wantCode, out)
			}
			for _, w := range tc.wantOut {
				if !strings.Contains(out, w) {
					t.Fatalf("输出缺 %q：\n%s", w, out)
				}
			}
			// 令牌不进任何输出
			if i := slices.Index(tc.args, "--token"); i >= 0 && i+1 < len(tc.args) && strings.Contains(out, tc.args[i+1]) {
				t.Fatalf("输出里出现了令牌：\n%s", out)
			}
		})
	}

	// 没渲染的模板（直接拿仓库里的文件跑）拒绝继续
	raw := writeScript(t, pdndInstallTemplate)
	out, code := runScript(t, bash, raw, []string{"PANDORA_INSTALL_DRY_RUN=1", "PANDORA_PANEL_URL="}, full(validPanel, validCA, validToken)...)
	if code != 2 {
		t.Fatalf("未渲染的模板应退出 2，得到 %d：\n%s", code, out)
	}
}

func TestInstallScriptRenderConfig(t *testing.T) {
	bash := bashOrSkip(t)
	script := writeRenderedScript(t)
	dir := t.TempDir()
	const newAddr = "new.example.com:9443"
	render := func(src, dst string) string {
		t.Helper()
		out, code := runLib(t, bash, script, `PANEL_ADDR="$3"; pandora_render_config "$1" "$2"`, nil, src, dst, newAddr)
		if code != 0 {
			t.Fatalf("pandora_render_config 退出 %d：\n%s", code, out)
		}
		return out
	}

	// 没有旧配置：生成的文件能被 pdnd 的严格解析（未知字段报错）读出
	dst := filepath.Join(dir, "fresh.json")
	render(filepath.Join(dir, "absent.json"), dst)
	raw, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var fresh struct {
		LogLevel string `json:"log_level"`
		Panel    struct {
			Address     string `json:"address"`
			IdentityDir string `json:"identity_dir"`
		} `json:"panel"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fresh); err != nil {
		t.Fatalf("新配置解析失败: %v\n%s", err, raw)
	}
	if fresh.Panel.Address != newAddr || fresh.Panel.IdentityDir != "/var/lib/pandora-native/identity" {
		t.Fatalf("新配置 %+v", fresh)
	}

	// 已有配置：只改 panel.address，其余键原样保留
	src := filepath.Join(dir, "existing.json")
	existing := `{
  "log_level": "debug",
  "panel": {
    "address": "old.example.com:9443",
    "identity_dir": "/var/lib/pandora-native/identity"
  },
  "runtime": {
    "drain_seconds": 5
  }
}
`
	if err := os.WriteFile(src, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	dst = filepath.Join(dir, "updated.json")
	render(src, dst)
	raw, err = os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("改写后的配置解析失败: %v\n%s", err, raw)
	}
	panel, _ := m["panel"].(map[string]any)
	rt, _ := m["runtime"].(map[string]any)
	if panel["address"] != newAddr || m["log_level"] != "debug" || rt["drain_seconds"] != float64(5) {
		t.Fatalf("改写结果 %s", raw)
	}

	// 找不到唯一的 address：原样保留并提示
	src = filepath.Join(dir, "noaddr.json")
	if err := os.WriteFile(src, []byte(`{"log_level":"info"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst = filepath.Join(dir, "noaddr.out.json")
	out := render(src, dst)
	got, _ := os.ReadFile(dst)
	want, _ := os.ReadFile(src)
	if string(got) != string(want) || !strings.Contains(out, "panel.address") {
		t.Fatalf("应原样保留并提示：%q\n%s", got, out)
	}
}

func TestInstallScriptRejectsForeignIdentityDir(t *testing.T) {
	bash := bashOrSkip(t)
	script := writeRenderedScript(t)
	dir := t.TempDir()
	env := []string{"PANDORA_CONFIG_DIR=" + dir}
	check := func() (string, int) {
		return runLib(t, bash, script, `pandora_check_existing_config`, env)
	}

	if out, code := check(); code != 0 {
		t.Fatalf("没有配置应通过：%d\n%s", code, out)
	}
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{"panel":{"address":"a.example.com:9443","identity_dir":"/srv/other"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := check(); code == 0 || !strings.Contains(out, "/srv/other") {
		t.Fatalf("别处的身份目录应拒绝：%d\n%s", code, out)
	}
	if err := os.WriteFile(cfg, []byte(`{"panel":{"address":"a.example.com:9443","identity_dir":"/var/lib/pandora-native/identity"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := check(); code != 0 {
		t.Fatalf("缺省身份目录应通过：%d\n%s", code, out)
	}
}

// 系统参数：在桩 sysctl 上只跑那一段函数，看 drop-in 的内容——缺的补上、小的调大、
// 机器上已有更大的值原样保留（只调大不调小），并且立即应用一次。
func TestInstallScriptSysctlDropInOnlyRaises(t *testing.T) {
	bash := bashOrSkip(t)
	script := writeRenderedScript(t)
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(root, "applied")
	// 当前值：conntrack 只有 1c1g 镜像的 8192；somaxconn、wmem_max 已经比目标大；
	// 端口范围比目标窄；netdev_max_backlog 读不到（模块未加载之类）。
	stub := `#!/bin/sh
if [ "$1" = "-n" ]; then
  case "$2" in
    net.netfilter.nf_conntrack_max) echo 8192 ;;
    net.core.somaxconn) echo 131072 ;;
    net.ipv4.tcp_max_tw_buckets) echo 32768 ;;
    net.ipv4.tcp_max_syn_backlog) echo 4096 ;;
    net.core.rmem_max) echo 212992 ;;
    net.core.wmem_max) echo 33554432 ;;
    net.ipv4.ip_local_port_range) printf '32768\t60999\n' ;;
    *) exit 1 ;;
  esac
  exit 0
fi
echo "$*" >> "` + applied + `"
`
	if err := os.WriteFile(filepath.Join(fakeBin, "sysctl"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	hashsize := filepath.Join(root, "hashsize")
	if err := os.WriteFile(hashsize, []byte("2048\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sysctlDir, modprobeDir := filepath.Join(root, "sysctl.d"), filepath.Join(root, "modprobe.d")
	out, code := runLib(t, bash, script, `pandora_tune_sysctl`, []string{
		"PATH=" + fakeBin + ":" + os.Getenv("PATH"),
		"PANDORA_SYSCTL_DIR=" + sysctlDir,
		"PANDORA_MODPROBE_DIR=" + modprobeDir,
		"PANDORA_CONNTRACK_HASHSIZE_PATH=" + hashsize,
	})
	if code != 0 {
		t.Fatalf("sysctl 段执行失败：%d\n%s", code, out)
	}
	raw, err := os.ReadFile(filepath.Join(sysctlDir, "90-pandora-native.conf"))
	if err != nil {
		t.Fatal(err)
	}
	conf := string(raw)
	for _, line := range []string{
		"net.netfilter.nf_conntrack_max = 524288",
		"net.ipv4.tcp_max_tw_buckets = 262144",
		"net.core.somaxconn = 131072", // 已有更大的值，不调小
		"net.ipv4.tcp_max_syn_backlog = 65535",
		"net.core.rmem_max = 16777216",
		"net.core.wmem_max = 33554432", // 已有更大的值，不调小
		"net.core.netdev_max_backlog = 16384",
		"net.ipv4.ip_local_port_range = 1024 65535",
		"net.ipv4.tcp_tw_reuse = 1",
	} {
		if !strings.Contains(conf, line+"\n") {
			t.Fatalf("drop-in 缺少 %q：\n%s", line, conf)
		}
	}
	modprobe, err := os.ReadFile(filepath.Join(modprobeDir, "pandora-native.conf"))
	if err != nil || strings.TrimSpace(string(modprobe)) != "options nf_conntrack hashsize=131072" {
		t.Fatalf("modprobe 参数=%q err=%v", modprobe, err)
	}
	if got, _ := os.ReadFile(hashsize); strings.TrimSpace(string(got)) != "131072" {
		t.Fatalf("已加载的 conntrack hashsize 应立即调大，got %q", got)
	}
	if got, _ := os.ReadFile(applied); !strings.Contains(string(got), "-e -p "+filepath.Join(sysctlDir, "90-pandora-native.conf")) {
		t.Fatalf("drop-in 应立即应用一次（-e 忽略未加载模块的键），got %q", got)
	}
}

func heredocBody(t *testing.T, marker string) string {
	t.Helper()
	startTag := "<<" + marker + "\n"
	idx := strings.Index(pdndInstallTemplate, startTag)
	if idx < 0 { // 不做展开的 heredoc（<<'MARK'）
		startTag = "<<'" + marker + "'\n"
		idx = strings.Index(pdndInstallTemplate, startTag)
	}
	if idx < 0 {
		t.Fatalf("脚本里找不到 heredoc 标记 %q", startTag)
	}
	bodyStart := idx + len(startTag)
	endTag := "\n" + marker + "\n"
	endIdx := strings.Index(pdndInstallTemplate[bodyStart:], endTag)
	if endIdx < 0 {
		t.Fatalf("脚本里 heredoc %q 没有结束标记 %q", marker, endTag)
	}
	return pdndInstallTemplate[bodyStart : bodyStart+endIdx]
}

func packagedUnit(t *testing.T, name string) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	content, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "..", "pdnd", "release", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func assertUnitsEqual(t *testing.T, script, packaged string) {
	t.Helper()
	got, want := unitLines(script), unitLines(packaged)
	if !slices.Equal(got, want) {
		var onlyScript, onlyPackaged []string
		for _, l := range got {
			if !slices.Contains(want, l) {
				onlyScript = append(onlyScript, l)
			}
		}
		for _, l := range want {
			if !slices.Contains(got, l) {
				onlyPackaged = append(onlyPackaged, l)
			}
		}
		t.Fatalf("unit 与发布包不一致\n只在脚本里：%q\n只在发布包里：%q", onlyScript, onlyPackaged)
	}
}

// 安装器写的主 unit 与发布包里的 pdnd/release/pandora-native.service 逐行一致（顺序不计）。
func TestInstallScriptUnitMatchesPackaged(t *testing.T) {
	unit := strings.NewReplacer(
		"${INSTALL_DIR}", "/usr/local/bin",
		"${SERVICE}", "pandora-native",
		"${CONFIG_DIR}", "/etc/pandora-native",
	).Replace(heredocBody(t, "UNIT"))
	assertUnitsEqual(t, unit, packagedUnit(t, "pandora-native.service"))
}

// 安装器写的升级触发器与助手 unit 与发布包逐行一致（顺序不计）。
func TestInstallScriptUpgradeUnitsMatchPackaged(t *testing.T) {
	repl := strings.NewReplacer(
		"${INSTALL_DIR}", "/usr/local/bin",
		"${SERVICE}", "pandora-native",
		"${CONFIG_DIR}", "/etc/pandora-native",
	)

	pathUnit := repl.Replace(heredocBody(t, "PATHUNIT"))
	assertUnitsEqual(t, pathUnit, packagedUnit(t, "pandora-native-upgrade.path"))

	helperUnit := repl.Replace(heredocBody(t, "HELPERUNIT"))
	assertUnitsEqual(t, helperUnit, packagedUnit(t, "pandora-native-upgrade.service"))
}

// 检查发布包里的升级单元与主单元的关键契约（触发路径、oneshot 助手、崩溃循环限制等）。
func TestPackagedUpgradeUnitsContract(t *testing.T) {
	pathContent := packagedUnit(t, "pandora-native-upgrade.path")
	for _, want := range []string{
		"PathExists=/var/lib/pandora-native/upgrade/request",
		"PathExists=/var/lib/pandora-native-upgrade/applying",
		"Unit=pandora-native-upgrade.service",
	} {
		if !strings.Contains(pathContent, want) {
			t.Errorf("path 单元缺少 %q", want)
		}
	}

	serviceContent := packagedUnit(t, "pandora-native-upgrade.service")
	for _, want := range []string{
		"Type=oneshot",
		"User=root",
		// 续做交给 .prev（新版一启动就崩时照样能回退），新请求用装着的版本。整行钉死：条件必须看 root 的工作区
		// （看 pandora 可写的暂存目录，等于让 pandora 决定 root 跑哪一版、拿更老的 serial 验签），两个分支不能写反
		"ExecStart=/bin/sh -c 'if [ -e /var/lib/pandora-native-upgrade/applying ] && [ -x /usr/local/bin/pandora-native.prev ]; then exec /usr/local/bin/pandora-native.prev upgrade-apply; fi; exec /usr/local/bin/pandora-native upgrade-apply'",
		"TimeoutStartSec=600",
		// 加固（upgrade-node 审查 S6）
		"ProtectSystem=strict",
		"ReadWritePaths=/usr/local/bin -/var/lib/pandora-native",
		"CapabilityBoundingSet=CAP_DAC_OVERRIDE",
		"PrivateNetwork=true",
		"RestrictAddressFamilies=AF_UNIX",
		"NoNewPrivileges=true",
		"PrivateDevices=true",
		"ProtectKernelTunables=true",
		"ProtectKernelModules=true",
		"ProtectKernelLogs=true",
		"ProtectControlGroups=true",
		"ProtectClock=true",
		"ProtectHostname=true",
		"RestrictNamespaces=true",
		"RestrictRealtime=true",
		"RestrictSUIDSGID=true",
		"LockPersonality=true",
		"MemoryDenyWriteExecute=true",
		"SystemCallArchitectures=native",
	} {
		if !strings.Contains(serviceContent, want) {
			t.Errorf("升级 service 单元缺少 %q", want)
		}
	}
	if strings.Contains(serviceContent, "[Install]") {
		t.Errorf("升级 service 单元不应包含 [Install]")
	}

	mainUnitContent := packagedUnit(t, "pandora-native.service")
	for _, want := range []string{
		"StartLimitIntervalSec=300",
		"StartLimitBurst=10",
	} {
		if !strings.Contains(mainUnitContent, want) {
			t.Errorf("主 service 单元缺少 %q", want)
		}
	}
}

// unitLines 取非空、非注释的行（去首尾空白）并排序。
func unitLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}

func TestInstallScriptRejectsUnrewritableAddress(t *testing.T) {
	bash := bashOrSkip(t)
	script := writeRenderedScript(t)
	dir := t.TempDir()
	env := []string{"PANDORA_CONFIG_DIR=" + dir}
	check := func() (string, int) {
		return runLib(t, bash, script, `PANEL_ADDR=new.example.com:9443; pandora_check_existing_config`, env)
	}
	cfg := filepath.Join(dir, "config.json")
	// 两个 address 键、都不是新地址：用掉令牌之前就要停下
	if err := os.WriteFile(cfg, []byte(`{"panel":{"address":"old.example.com:9443"},"runtime":{"address":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := check(); code == 0 || !strings.Contains(out, "new.example.com:9443") {
		t.Fatalf("改不了地址应拒绝：%d\n%s", code, out)
	}
	// 本来就是新地址：放行
	if err := os.WriteFile(cfg, []byte(`{"panel":{"address":"new.example.com:9443"},"runtime":{"address":"x"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := check(); code != 0 {
		t.Fatalf("已是新地址应通过：%d\n%s", code, out)
	}
}
