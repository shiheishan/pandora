package nodefabric

import (
	"strings"
	"testing"
)

// 这段渲染的产物是要被人复制到 root shell 里执行的。渲染错了不会有
// 报错，只会在某台机器上跑出意料之外的命令，所以每种取值都钉死。
func TestRenderInstallCommand(t *testing.T) {
	const panel = "https://pandora.example.com"

	t.Run("默认模板", func(t *testing.T) {
		got := renderInstallCommand("", panel, "tok-123", "hk-01")
		if !strings.Contains(got, "Bootstrap token:") || !strings.Contains(got, "</dev/tty") || !strings.Contains(got, "trap '") || !strings.Contains(got, "--token-file") ||
			!strings.Contains(got, "--name 'hk-01'") || strings.Contains(got, "tok-123") {
			t.Errorf("默认命令必须交互读取令牌且不得嵌入明文：%q", got)
		}
	})

	// 面板地址通常会被拼进更长的 URL。加了引号 shell 也能解析，
	// 但复制出来像坏的，会有人手动去掉引号顺手改错别的地方。
	t.Run("干净的面板地址不加引号", func(t *testing.T) {
		got := renderInstallCommand("curl -fsSL {{PANEL}}/install.sh", panel, "t", "n")
		want := "curl -fsSL https://pandora.example.com/install.sh"
		if got != want {
			t.Errorf("得到 %q，期望 %q", got, want)
		}
	})

	// 反过来，地址一旦不在白名单字符集里就必须引起来——哪怕这种输入
	// 只可能来自畸形的 X-Forwarded-Host，也不能让它拼出可执行的东西。
	t.Run("可疑的面板地址要引起来", func(t *testing.T) {
		got := renderInstallCommand("run {{PANEL}}", "https://evil.com; rm -rf /", "t", "n")
		want := `run 'https://evil.com; rm -rf /'`
		if got != want {
			t.Errorf("得到 %q，期望 %q", got, want)
		}
	})

	// 服务器名是管理员随手起的，带空格是常态。
	t.Run("名字里的空格", func(t *testing.T) {
		got := renderInstallCommand("x --name {{NAME}}", panel, "t", "香港 01")
		if got != `x --name '香港 01'` {
			t.Errorf("带空格的名字没被引起来：%q", got)
		}
	})

	// 单引号是唯一能从单引号串里逃出去的字符。
	t.Run("名字里的单引号", func(t *testing.T) {
		got := renderInstallCommand("x --name {{NAME}}", panel, "t", "it's")
		if got != `x --name 'it'\''s'` {
			t.Errorf("单引号转义不对：%q", got)
		}
	})

	// 令牌是随机 base64，字符集里有 - 和 _，不引也不会出事，
	// 但它是一等一的敏感值，不该依赖「碰巧安全」。
	t.Run("令牌始终引起来", func(t *testing.T) {
		got := renderInstallCommand("x --token {{TOKEN}}", panel, "aB3-_x", "n")
		if got != `x --token 'aB3-_x'` {
			t.Errorf("令牌没被引起来：%q", got)
		}
	})

	// 换内核就是换这一行配置，这是整个改动的目的。
	t.Run("自定义模板", func(t *testing.T) {
		tpl := "curl -fsSL {{PANEL}}/pdnd/install.sh | sudo bash -s -- " +
			"--panel {{PANEL}} --token {{TOKEN}} --name {{NAME}}"
		got := renderInstallCommand(tpl, panel, "tk", "jp-02")
		want := "curl -fsSL https://pandora.example.com/pdnd/install.sh | sudo bash -s -- " +
			"--panel https://pandora.example.com --token 'tk' --name 'jp-02'"
		if got != want {
			t.Errorf("得到 %q，期望 %q", got, want)
		}
	})

	t.Run("只有空白的模板退回默认", func(t *testing.T) {
		got := renderInstallCommand("   \n  ", panel, "t", "n")
		if got == "   \n  " || got == "" {
			t.Errorf("空白模板应当退回默认，实际 %q", got)
		}
	})
}

func TestRenderLegacyInstallCommandDoesNotExposeRuntimeToken(t *testing.T) {
	got := RenderLegacyInstallCommand("https://pandora.example.com", "node-1", "vless")
	for _, forbidden := range []string{"--token ", "runtime-token", "PANDORA_RUNTIME_TOKEN="} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("legacy command exposed forbidden token material %q: %s", forbidden, got)
		}
	}
	for _, required := range []string{"UniProxy runtime token:", "</dev/tty", "trap '", "--token-file \"$PANDORA_TOKEN_FILE\"", "rm -f \"$PANDORA_TOKEN_FILE\""} {
		if !strings.Contains(got, required) {
			t.Fatalf("legacy command missing %q: %s", required, got)
		}
	}
}

func TestSafePanelURL(t *testing.T) {
	ok := []string{
		"https://pandora.example.com",
		"http://127.0.0.1:9001",
		"https://panel.example.com:8443",
		"https://[2001:db8::1]:443",
		"https://a-b_c.example.com",
	}
	for _, u := range ok {
		if !safePanelURL.MatchString(u) {
			t.Errorf("%q 是正常地址，不该被判为可疑", u)
		}
	}
	bad := []string{
		"https://x.com; rm -rf /",
		"https://x.com`id`",
		"https://x.com$(id)",
		"https://x.com 'y'",
		"ftp://x.com",
		"https://x.com/path", // 带路径的不在白名单里：模板会自己拼路径
		"",
	}
	for _, u := range bad {
		if safePanelURL.MatchString(u) {
			t.Errorf("%q 应当被判为可疑并引起来", u)
		}
	}
}
