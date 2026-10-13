package service

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

func TestPdndArtifactAllowed(t *testing.T) {
	if !PdndArtifactAllowed(PdndArtifactAMD64) || !PdndArtifactAllowed(PdndArtifactARM64) {
		t.Fatal("合法产物名应通过白名单")
	}
	bad := []string{
		"",
		"pandora-native-linux-386",
		"../pandora-native-linux-amd64",
		"pandora-native-linux-amd64/",
		"PANDORA-NATIVE-LINUX-AMD64",
		"pandora-native-linux-amd64.sha256",
	}
	for _, name := range bad {
		if PdndArtifactAllowed(name) {
			t.Fatalf("%q 不应在白名单内", name)
		}
	}
}

func TestOpenPdndArtifact(t *testing.T) {
	dir := t.TempDir()
	amd64Path := dir + "/" + PdndArtifactAMD64
	if err := os.WriteFile(amd64Path, []byte("amd64-bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir+"/"+PdndArtifactARM64, 0o700); err != nil {
		t.Fatal(err)
	}

	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")

	a, err := s.OpenPdndArtifact(PdndArtifactAMD64)
	if err != nil {
		t.Fatalf("amd64: %v", err)
	}
	if a.Size != int64(len("amd64-bin\n")) {
		t.Fatalf("Size: got %d", a.Size)
	}
	b, err := io.ReadAll(a.File)
	_ = a.File.Close()
	if err != nil || string(b) != "amd64-bin\n" {
		t.Fatalf("内容: %v %q", err, b)
	}

	_, err = s.OpenPdndArtifact(PdndArtifactARM64)
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("arm64 目录: code=%v", errs.As(err).Code)
	}

	_, err = s.OpenPdndArtifact("pandora-native-linux-386")
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("非白名单: code=%v", errs.As(err).Code)
	}
	if strings.Contains(errs.As(err).Message, dir) {
		t.Fatal("非白名单错误消息不应包含目录路径")
	}

	dirOnly := t.TempDir()
	s2 := New(nil, nil, nil, nil)
	s2.UsePdndDist(dirOnly, "https://panel.example.com")
	_, errMissing := s2.OpenPdndArtifact(PdndArtifactAMD64)
	if errs.As(errMissing).Code != errs.NotFound {
		t.Fatalf("缺文件: %v", errMissing)
	}
	if strings.Contains(errs.As(errMissing).Message, dirOnly) {
		t.Fatal("缺文件时错误消息不应包含目录路径")
	}
}

func TestPdndChecksum(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/"+PdndArtifactAMD64, []byte("amd64-bin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com")

	want := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte("amd64-bin\n")), PdndArtifactAMD64)
	got, err := s.PdndChecksum(PdndArtifactAMD64)
	if err != nil || got != want {
		t.Fatalf("checksum: err=%v got=%q want=%q", err, got, want)
	}

	_, err = s.PdndChecksum(PdndArtifactARM64)
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("缺文件: %v", err)
	}
}

func TestPdndOrigin(t *testing.T) {
	reject := []string{
		"",
		"http://panel.example.com",
		"ftp://panel.example.com",
		"https://",
		"https://user@panel.example.com",
		"https://panel.example.com?x=1",
		"https://panel.example.com/#f",
		"https://panel.example.com/$(id)",
		"https://panel.example.com/`id`",
		"https://panel.example.com/\"x",
		"https://panel.example.com/a b",
	}
	for _, raw := range reject {
		_, err := pdndOrigin(raw)
		if err == nil {
			t.Fatalf("应拒绝 %q", raw)
		}
	}

	cases := []struct {
		in, want string
	}{
		{"https://panel.example.com/", "https://panel.example.com"},
		{" https://panel.example.com:8443/base/ ", "https://panel.example.com:8443/base"},
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000"},
		{"http://localhost:9000", "http://localhost:9000"},
		{"http://[::1]:9000", "http://[::1]:9000"},
	}
	for _, tc := range cases {
		got, err := pdndOrigin(tc.in)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("%q: got %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestPdndDistNotWired(t *testing.T) {
	s := New(nil, nil, nil, nil)

	_, err := s.OpenPdndArtifact(PdndArtifactAMD64)
	if errs.As(err).Code != errs.NotFound {
		t.Fatalf("Open: %v", err)
	}

	_, err = s.PdndInstallScript()
	if errs.As(err).Code != errs.Unavailable {
		t.Fatalf("InstallScript: %v", err)
	}
}

func TestPdndInstallScriptRendersOrigin(t *testing.T) {
	dir := t.TempDir()
	s := New(nil, nil, nil, nil)
	s.UsePdndDist(dir, "https://panel.example.com/")

	script, err := s.PdndInstallScript()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(script, "#!/usr/bin/env bash") {
		t.Fatal("应以 shebang 开头")
	}
	if !strings.Contains(script, `PANEL_ORIGIN_DEFAULT="https://panel.example.com"`) {
		t.Fatal("应渲染默认面板地址")
	}
	if strings.Contains(script, pdndOriginPlaceholder) {
		t.Fatal("不应残留占位符")
	}

	s2 := New(nil, nil, nil, nil)
	s2.UsePdndDist(dir, "http://panel.example.com")
	_, err = s2.PdndInstallScript()
	if errs.As(err).Code != errs.Unavailable {
		t.Fatalf("非本机 http 应不可用: %v", err)
	}
}
