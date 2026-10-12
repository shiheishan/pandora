package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 装配冒烟：不连库也能建出全部服务与两个引擎（模块间路由重复时 gin 会 panic），关键路由都在。
func TestEnginesRegisterModuleRoutes(t *testing.T) {
	t.Setenv("PANDORA_ENV", "development")
	t.Setenv("PANDORA_DATABASE_URL", "postgres://u:p@127.0.0.1:5432/db")
	t.Setenv("PANDORA_REDIS_URL", "redis://127.0.0.1:6379/0")
	t.Setenv("PANDORA_SECRETS_MASTER_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	t.Setenv("PANDORA_AUTH_PUBLIC_JWT_SECRET", strings.Repeat("a", 32))
	t.Setenv("PANDORA_AUTH_ADMIN_JWT_SECRET", strings.Repeat("b", 32))
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = rdb.Close() })
	r := &runtime{cfg: cfg, log: zap.NewNop(), redis: rdb}

	svc, err := buildServices(r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.close)
	publicEng, adminEng := newEngines(r, svc)

	want := map[*gin.Engine][]string{
		publicEng: {
			"POST /v1/auth/login", "POST /v1/auth/refresh", "POST /v1/auth/logout", "POST /v1/auth/quick-login",
			"POST /v1/auth/register/start", "POST /v1/auth/password-reset/start", "GET /v1/site-config",
		},
		adminEng: {
			"POST /v1/auth/login", "POST /v1/auth/refresh", "POST /v1/auth/reauth", "GET /v1/users",
			"GET /v1/audit", "GET /v1/switches", "POST /v1/settings/site", "GET /v1/settings/mail",
			"GET /v1/mail/templates",
		},
	}
	for eng, routes := range want {
		have := map[string]bool{}
		for _, ri := range eng.Routes() {
			have[ri.Method+" "+ri.Path] = true
		}
		for _, rt := range routes {
			if !have[rt] {
				t.Errorf("缺路由 %s", rt)
			}
		}
	}
}

func TestReadPasswordLine(t *testing.T) {
	for in, want := range map[string]string{"pw\n": "pw", "pw\r\n": "pw", "pw": "pw"} {
		got, err := readPasswordLine(strings.NewReader(in))
		if err != nil || got != want {
			t.Fatalf("%q → %q, %v", in, got, err)
		}
	}
	if _, err := readPasswordLine(strings.NewReader("\n")); err == nil {
		t.Fatal("空口令应报错")
	}
}

func TestAdminCommandsAndExitCode(t *testing.T) {
	cmd := newAdminCmd(new(string))
	names := map[string]bool{}
	for _, c := range cmd.Commands() {
		names[c.Name()] = true
	}
	for _, n := range []string{"create", "reset-password", "grant", "revoke", "list", "roles", "has-admin"} {
		if !names[n] {
			t.Errorf("缺子命令 %s", n)
		}
	}
	if exitCode(&exitCodeError{code: exitNoAdministrator}) != 3 || exitCode(errors.New("x")) != 1 {
		t.Fatal("退出码")
	}
}
