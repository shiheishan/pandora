package main

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadPasswordFile(t *testing.T) {
	tmp := t.TempDir()
	okPath := filepath.Join(tmp, "ok")
	if err := os.WriteFile(okPath, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pwd, err := readPasswordFile(okPath)
	if err != nil || pwd != "secret" {
		t.Fatalf("ok: pwd=%q err=%v", pwd, err)
	}
	crlfPath := filepath.Join(tmp, "crlf")
	if err := os.WriteFile(crlfPath, []byte("secret\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pwd, err = readPasswordFile(crlfPath)
	if err != nil || pwd != "secret" {
		t.Fatalf("crlf: pwd=%q err=%v", pwd, err)
	}

	permPath := filepath.Join(tmp, "perm")
	if err := os.WriteFile(permPath, []byte("leakcheck-secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = readPasswordFile(permPath)
	if err == nil {
		t.Fatal("expected perm error")
	}
	if strings.Contains(err.Error(), "leakcheck-secret") {
		t.Fatalf("perm error leaks content: %v", err)
	}

	emptyPath := filepath.Join(tmp, "empty")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readPasswordFile(emptyPath)
	if err == nil {
		t.Fatal("expected empty error")
	}

	multiPath := filepath.Join(tmp, "multi")
	if err := os.WriteFile(multiPath, []byte("leakone\nleaktwo"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readPasswordFile(multiPath)
	if err == nil {
		t.Fatal("expected multiline error")
	}
	if strings.Contains(err.Error(), "leakone") {
		t.Fatalf("multiline error leaks content: %v", err)
	}

	long := strings.Repeat("a", 257)
	longPath := filepath.Join(tmp, "long")
	if err := os.WriteFile(longPath, []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readPasswordFile(longPath)
	if err == nil {
		t.Fatal("expected long error")
	}
	if strings.Contains(err.Error(), long[:10]) {
		t.Fatalf("long error leaks content: %v", err)
	}
}

func TestReadDSNFile(t *testing.T) {
	tmp := t.TempDir()
	raw := "postgres://user:pass@127.0.0.1:5432/db?sslmode=disable"
	rawPath := filepath.Join(tmp, "raw")
	if err := os.WriteFile(rawPath, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err := readDSNFile(rawPath)
	if err != nil || dsn != raw {
		t.Fatalf("raw: dsn=%q err=%v", dsn, err)
	}

	env := "# comment\nFOO=bar\nPANDORA_DATABASE_MIGRATION_URL=postgres://pandora_owner:x@127.0.0.1:5432/pandora?sslmode=disable\n"
	envPath := filepath.Join(tmp, "env")
	if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	dsn, err = readDSNFile(envPath)
	want := "postgres://pandora_owner:x@127.0.0.1:5432/pandora?sslmode=disable"
	if err != nil || dsn != want {
		t.Fatalf("env: dsn=%q err=%v", dsn, err)
	}

	badPath := filepath.Join(tmp, "bad")
	if err := os.WriteFile(badPath, []byte("mysql://x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = readDSNFile(badPath)
	if err == nil {
		t.Fatal("expected scheme error")
	}
	if strings.Contains(err.Error(), "mysql") {
		t.Fatalf("scheme error leaks content: %v", err)
	}
}

func TestReadEnvValue(t *testing.T) {
	tmp := t.TempDir()
	p := filepath.Join(tmp, "e")
	content := "# skip\n\nFOO=old\nBAR=baz\nFOO=final\r\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := readEnvValue(p, "FOO")
	if err != nil || v != "final" {
		t.Fatalf("FOO: v=%q err=%v", v, err)
	}
	v, err = readEnvValue(p, "BAR")
	if err != nil || v != "baz" {
		t.Fatalf("BAR: v=%q err=%v", v, err)
	}
	_, err = readEnvValue(p, "MISSING")
	if err == nil {
		t.Fatal("expected missing key")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("missing error leaks value: %v", err)
	}
}

func TestWriteFileAtomic(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "out")
	data := []byte("hello")
	if err := writeFileAtomic(path, data); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content: %q err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm: %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Fatalf("leftover tmp: %s", e.Name())
		}
	}
}

func TestSaveLoadJSON(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "infra.json")
	var missing InfraState
	ok, err := loadJSON(path, &missing)
	if err != nil || ok {
		t.Fatalf("missing: ok=%v err=%v", ok, err)
	}
	want := InfraState{GroupID: "g1", TokensDone: true}
	if err := saveJSON(path, want); err != nil {
		t.Fatal(err)
	}
	var got InfraState
	ok, err = loadJSON(path, &got)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got.GroupID != want.GroupID || got.TokensDone != want.TokensDone {
		t.Fatalf("roundtrip: %+v", got)
	}
}

func parseTokensLikeNodesim(data []byte) error {
	lines := strings.Split(string(data), "\n")
	seen := map[string]struct{}{}
	count := 0
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return fmt.Errorf("fields %d", len(fields))
		}
		if !validTokenName(fields[0]) {
			return fmt.Errorf("bad name")
		}
		if _, ok := seen[fields[0]]; ok {
			return fmt.Errorf("dup")
		}
		seen[fields[0]] = struct{}{}
		count++
	}
	if count == 0 {
		return fmt.Errorf("empty")
	}
	return nil
}

func TestFormatTokens(t *testing.T) {
	entries := []TokenEntry{{Name: "s0001", Token: "tok1"}, {Name: "s0002", Token: "tok2"}}
	b, err := formatTokens(entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := parseTokensLikeNodesim(b); err != nil {
		t.Fatal(err)
	}

	_, err = formatTokens(nil)
	if err == nil {
		t.Fatal("empty list")
	}
	_, err = formatTokens([]TokenEntry{{Name: "s0001", Token: "a"}, {Name: "s0001", Token: "b"}})
	if err == nil {
		t.Fatal("dup name")
	}
	_, err = formatTokens([]TokenEntry{{Name: "bad name", Token: "t"}})
	if err == nil {
		t.Fatal("bad name")
	}
	_, errSpace := formatTokens([]TokenEntry{{Name: "s1", Token: "has space"}})
	if errSpace == nil {
		t.Fatal("space in token")
	}
	if strings.Contains(errSpace.Error(), "has space") {
		t.Fatalf("token leak: %v", errSpace)
	}
}

func TestParseInstallCommand(t *testing.T) {
	cmd := "curl -fsSL 'https://panel.example.com/x' | sudo bash -s -- --panel 'panel.example.com:9443' --ca-sha256 'sha256:abcd' --token 'tok'"
	panel, ca, err := parseInstallCommand(cmd)
	if err != nil || panel != "panel.example.com:9443" || ca != "sha256:abcd" {
		t.Fatalf("ok: panel=%q ca=%q err=%v", panel, ca, err)
	}
	_, _, err = parseInstallCommand("--ca-sha256 'sha256:x'")
	if err == nil {
		t.Fatal("missing panel")
	}
	_, _, err = parseInstallCommand("--panel 'p:1'")
	if err == nil {
		t.Fatal("missing ca")
	}
	_, _, err = parseInstallCommand("--panel 'p:1' --ca-sha256 'abcd'")
	if err == nil {
		t.Fatal("bad ca prefix")
	}
	_, _, err = parseInstallCommand("--panel 'p' --ca-sha256 'sha256:x' --token 'tok'")
	if err != nil && strings.Contains(err.Error(), "tok") {
		t.Fatalf("leaks token: %v", err)
	}
}

func TestFormatNodesimEnv(t *testing.T) {
	got := string(formatNodesimEnv("10.0.0.1:9443", "sha256:deadbeef"))
	if want := "PANEL=10.0.0.1:9443\nCA_SHA256=sha256:deadbeef\n"; got != want {
		t.Fatalf("nodesim.env = %q, want %q", got, want)
	}
}

func TestWriteUsersCSV(t *testing.T) {
	var buf bytes.Buffer
	rows := []CSVRow{{UserID: "u1", Email: "a@example.com", SubToken: "st"}}
	if err := writeUsersCSV(&buf, "pw", rows); err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(&buf)
	rec, err := r.Read()
	if err != nil || len(rec) != 4 || rec[0] != "user_id" {
		t.Fatalf("header: %v err=%v", rec, err)
	}
	rec, err = r.Read()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"u1", "a@example.com", "pw", "st"}
	for i := range want {
		if rec[i] != want[i] {
			t.Fatalf("row: %v want %v", rec, want)
		}
	}
}

func TestServerNameUserEmail(t *testing.T) {
	if serverName(1) != "s0001" || serverName(200) != "s0200" {
		t.Fatal(serverName(1), serverName(200))
	}
	if userEmail(1) != "perf-00001@example.com" {
		t.Fatal(userEmail(1))
	}
}
