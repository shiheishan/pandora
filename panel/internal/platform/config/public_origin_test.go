package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 生产规则与两个部署脚本共用 deploy/fixtures/public-base-url-cases.txt：同一张表，
// shell 侧由 public-base-url_mock_test.sh 与 render-nginx_test.sh 逐行跑。
func TestCanonicalPublicOriginProductionMatchesDeployCases(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "deploy", "fixtures", "public-base-url-cases.txt"))
	if err != nil {
		t.Fatalf("open shared cases: %v", err)
	}
	defer f.Close()
	var accepted, rejected int
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		verdict, raw, ok := strings.Cut(line, " ")
		if !ok || (verdict != "accept" && verdict != "reject") {
			t.Fatalf("malformed case line %q", line)
		}
		got, err := (&Config{Env: "production", PublicBaseURL: raw}).CanonicalPublicOrigin()
		switch verdict {
		case "accept":
			accepted++
			if err != nil {
				t.Errorf("%s rejected: %v", raw, err)
			} else if want := strings.TrimRight(raw, "/"); got != want {
				t.Errorf("%s canonicalised to %q, want %q", raw, got, want)
			}
		case "reject":
			rejected++
			if err == nil {
				t.Errorf("%s accepted as %q", raw, got)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if accepted < 5 || rejected < 20 {
		t.Fatalf("shared case table looks truncated: %d accept, %d reject", accepted, rejected)
	}
}

// 非生产环境不按公网规则卡：本地开发用 http://127.0.0.1:9000 这类地址。
func TestCanonicalPublicOriginOutsideProductionKeepsLoopback(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:9000", "https://localhost:8443", "https://[::1]:9443/"} {
		if _, err := (&Config{Env: "development", PublicBaseURL: raw}).CanonicalPublicOrigin(); err != nil {
			t.Errorf("development rejected %s: %v", raw, err)
		}
	}
	if _, err := (&Config{Env: "development", PublicBaseURL: "https://panel.example.test/x"}).CanonicalPublicOrigin(); err == nil {
		t.Error("a path is never part of the origin, not even in development")
	}
}
