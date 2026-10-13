package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseTokensOK(t *testing.T) {
	input := `
# comment

  srv-a tok-a
srv-b	tok-b
srv-c tok-c
` + "srv-d tok-d\r\n"
	tokens, err := ParseTokens(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 4 {
		t.Fatalf("期望 4 条，实际 %d", len(tokens))
	}
	if tokens[0].Name != "srv-a" || tokens[0].Token != "tok-a" {
		t.Fatalf("第一条: %+v", tokens[0])
	}
	if tokens[1].Name != "srv-b" || tokens[1].Token != "tok-b" {
		t.Fatalf("第二条: %+v", tokens[1])
	}
	if tokens[2].Name != "srv-c" || tokens[2].Token != "tok-c" {
		t.Fatalf("第三条: %+v", tokens[2])
	}
	// CRLF 行尾的 \r 不能留在令牌里
	if tokens[3].Name != "srv-d" || tokens[3].Token != "tok-d" {
		t.Fatalf("第四条: %+v", tokens[3])
	}
}

func TestParseTokensDuplicateName(t *testing.T) {
	_, err := ParseTokens(strings.NewReader("a t1\na t2"))
	if err == nil {
		t.Fatal("应报错")
	}
	if strings.Contains(err.Error(), "t1") || strings.Contains(err.Error(), "t2") {
		t.Fatalf("错误信息不得含令牌: %v", err)
	}
}

func TestParseTokensFieldCount(t *testing.T) {
	_, err := ParseTokens(strings.NewReader("onlyone"))
	if err == nil {
		t.Fatal("1 字段应报错")
	}
	_, err = ParseTokens(strings.NewReader("a b c"))
	if err == nil {
		t.Fatal("3 字段应报错")
	}
}

func TestParseTokensInvalidName(t *testing.T) {
	_, err := ParseTokens(strings.NewReader("bad/name tok"))
	if err == nil {
		t.Fatal("含 / 应报错")
	}
	long := strings.Repeat("a", 65)
	_, err = ParseTokens(strings.NewReader(long + " tok"))
	if err == nil {
		t.Fatal("超过 64 字符应报错")
	}
}

func TestParseTokensEmpty(t *testing.T) {
	_, err := ParseTokens(strings.NewReader("\n# only comment\n"))
	if err == nil {
		t.Fatal("空文件应报错")
	}
}

func TestParseTokensErrorOmitsToken(t *testing.T) {
	secret := "secret-token-xyz"
	_, err := ParseTokens(strings.NewReader("bad/name " + secret))
	if err == nil {
		t.Fatal("应报错")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("错误信息不得含令牌: %v", err)
	}
}

func TestLoadTokens(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tokens.txt")
	if err := os.WriteFile(path, []byte("sim1 abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tokens, err := LoadTokens(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(tokens) != 1 || tokens[0].Name != "sim1" || tokens[0].Token != "abc123" {
		t.Fatalf("%+v", tokens)
	}
	_, err = LoadTokens(filepath.Join(dir, "missing"))
	if err == nil {
		t.Fatal("缺失文件应报错")
	}
	if !strings.Contains(err.Error(), path) && !strings.Contains(err.Error(), "missing") {
		t.Fatalf("错误应带 path: %v", err)
	}
}
