package public

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// 安装器的系统参数：在桩 sysctl 上只跑那一段函数，看 drop-in 的内容——缺的补上、
// 小的调大、机器上已有更大的值原样保留（只调大不调小），并且立即应用一次。
func TestPDNDInstallerSysctlDropInOnlyRaises(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	applied := filepath.Join(root, "applied")
	// 当前值：conntrack 只有 1c1g 镜像的 8192；somaxconn 已经比目标大；
	// 端口范围比目标窄；netdev_max_backlog 读不到（模块未加载之类）。
	writeSysctlStub(t, filepath.Join(fakeBin, "sysctl"), `#!/bin/sh
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
echo "$*" >> "`+applied+`"
`)
	hashsize := filepath.Join(root, "hashsize")
	if err := os.WriteFile(hashsize, []byte("2048\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sysctlDir, modprobeDir := filepath.Join(root, "sysctl.d"), filepath.Join(root, "modprobe.d")
	script := "#!/bin/sh\nset -eu\n" + pdndSysctlFunction + "\npandora_tune_sysctl\n"
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+":"+os.Getenv("PATH"),
		"PANDORA_SYSCTL_DIR="+sysctlDir,
		"PANDORA_MODPROBE_DIR="+modprobeDir,
		"PANDORA_CONNTRACK_HASHSIZE_PATH="+hashsize,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sysctl 段执行失败：%v\n%s", err, out)
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

// 安装流程里确实调用了它，且失败不挡安装。
func TestPDNDInstallerCallsSysctlTuning(t *testing.T) {
	if !strings.Contains(pdndInstallTemplate, "pandora_tune_sysctl() {") {
		t.Fatal("安装脚本没有带上系统参数函数")
	}
	if !strings.Contains(pdndInstallTemplate, `pandora_tune_sysctl || echo`) {
		t.Fatal("安装脚本没有调用系统参数函数，或调用失败会中断安装")
	}
}

func writeSysctlStub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}
