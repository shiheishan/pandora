package service

import (
	"strings"
	"testing"
)

func TestBuildInstallCommand(t *testing.T) {
	fp := "sha256:" + strings.Repeat("a", 64)
	got := buildInstallCommand(InstallTarget{BaseURL: "https://panel.example.test/", Host: "node.example.test", Port: 9443}, fp, "tok_ABC-123")
	want := "curl -fsSL 'https://panel.example.test/pdnd/install.sh' | sudo bash -s -- --panel 'node.example.test:9443' --ca-sha256 '" +
		fp + "' --token 'tok_ABC-123'"
	if got != want {
		t.Fatalf("安装命令:\n got %s\nwant %s", got, want)
	}
}

func TestBuildInstallCommandIPv6(t *testing.T) {
	got := buildInstallCommand(InstallTarget{BaseURL: "https://panel.example.test", Host: "2001:db8::1", Port: 9443}, "sha256:x", "t")
	if !strings.Contains(got, "--panel '[2001:db8::1]:9443'") {
		t.Fatalf("IPv6 要加方括号: %s", got)
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's"); got != `'it'\''s'` {
		t.Fatalf("shellQuote: %s", got)
	}
}
