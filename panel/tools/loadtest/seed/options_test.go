// [INPUT]: 依赖 options.go 的 parseOptions，用假环境变量表代替进程环境
// [OUTPUT]: 单测：flag 优先于环境变量、LOADTEST_* 优先于冒烟栈的 SMOKE_*、口令只从环境变量来、各项校验拒绝坏值
// [POS]: tools/loadtest/seed 的参数契约测试，保证 CI 冒烟能只靠 smoke.env 与 gateway.env 调起 seed

package seed

import (
	"io"
	"strings"
	"testing"
)

const testTenant = "00000000-0000-7000-8000-000000000001"

func fakeEnv(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func smokeEnv() map[string]string {
	return map[string]string{
		"AEGIS_DATABASE_URL":   "postgres://app@127.0.0.1:5432/aegis_smoke_test",
		"SMOKE_ADMIN_BASE":     "http://127.0.0.1:9001",
		"SMOKE_NODE_BASE":      "http://127.0.0.1:9003",
		"SMOKE_PUBLIC_BASE":    "http://127.0.0.1:9000",
		"SMOKE_ADMIN_EMAIL":    "smoke-admin@example.test",
		"SMOKE_ADMIN_PASSWORD": "not-a-secret-1",
	}
}

func TestParseOptionsFromSmokeEnv(t *testing.T) {
	o, err := parseOptions([]string{"-users", "200", "-nodes", "5", "-label", "ci", "-out", "/tmp/m.json"},
		fakeEnv(smokeEnv()), testTenant, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.DatabaseURL != smokeEnv()["AEGIS_DATABASE_URL"] || o.AdminBase != "http://127.0.0.1:9001" ||
		o.NodeBase != "http://127.0.0.1:9003" || o.PublicBase != "http://127.0.0.1:9000" ||
		o.AdminEmail != "smoke-admin@example.test" || o.AdminPassword != "not-a-secret-1" {
		t.Fatalf("smoke env not picked up: %+v", o)
	}
	if o.TenantID != testTenant || o.Users != 200 || o.Nodes != 5 || o.NodesPerServer != 1 ||
		o.Batch != defaultBatch || o.AdminInterval != defaultAdminInterval || !o.RetirePrevious || !o.Verify {
		t.Fatalf("defaults wrong: %+v", o)
	}
	if o.AgentVersion != "loadtest" || o.BinarySHA256 != strings.Repeat("1", 64) {
		t.Fatalf("enrollment evidence defaults wrong: %q %q", o.AgentVersion, o.BinarySHA256)
	}
}

func TestParseOptionsPrecedence(t *testing.T) {
	env := smokeEnv()
	env["LOADTEST_ADMIN_BASE"] = "https://panel.example.test/entry/"
	env["LOADTEST_ADMIN_PASSWORD"] = "not-a-secret-2"
	env["LOADTEST_DATABASE_URL"] = "postgres://loadtest@127.0.0.1/db"
	env["PANDORA_NATIVE_RELEASE_VERSION"] = "v9.9.9"
	env["PANDORA_NATIVE_ARTIFACT_AMD64_SHA256"] = strings.Repeat("AB", 32)
	o, err := parseOptions([]string{"-users", "1", "-label", "5k", "-out", "m.json",
		"-node-base", "https://node.example.test", "-admin-interval", "0s"}, fakeEnv(env), testTenant, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.AdminBase != "https://panel.example.test/entry" {
		t.Fatalf("LOADTEST_ADMIN_BASE should win over SMOKE_ADMIN_BASE and lose its trailing slash: %q", o.AdminBase)
	}
	if o.NodeBase != "https://node.example.test" {
		t.Fatalf("flag should win over env: %q", o.NodeBase)
	}
	if o.AdminPassword != "not-a-secret-2" || o.DatabaseURL != "postgres://loadtest@127.0.0.1/db" {
		t.Fatalf("LOADTEST_* should win: %+v", o)
	}
	if o.AgentVersion != "v9.9.9" || o.BinarySHA256 != strings.Repeat("ab", 32) || o.AdminInterval != 0 {
		t.Fatalf("release evidence or interval wrong: %+v", o)
	}
}

func TestParseOptionsRejectsBadInput(t *testing.T) {
	base := []string{"-users", "10", "-label", "ci", "-out", "m.json"}
	cases := []struct {
		name string
		args []string
		env  func(map[string]string)
		want string
	}{
		{"no users", []string{"-label", "ci", "-out", "m.json"}, nil, "-users"},
		{"too many users", []string{"-users", "131071", "-label", "ci", "-out", "m.json"}, nil, "-users"},
		{"label upper case", []string{"-users", "1", "-label", "CI", "-out", "m.json"}, nil, "-label"},
		{"label edge hyphen", []string{"-users", "1", "-label", "ci-", "-out", "m.json"}, nil, "-label"},
		{"label too long", []string{"-users", "1", "-label", strings.Repeat("a", 17), "-out", "m.json"}, nil, "-label"},
		{"no out", []string{"-users", "1", "-label", "ci"}, nil, "-out"},
		{"too many nodes", append([]string{"-nodes", "1501"}, base...), nil, "-nodes"},
		{"node base with path", append([]string{"-node-base", "http://127.0.0.1:9003/prefix"}, base...), nil, "-node-base"},
		{"bad digest", append([]string{"-binary-sha256", "xyz"}, base...), nil, "-binary-sha256"},
		{"no password", base, func(e map[string]string) { delete(e, "SMOKE_ADMIN_PASSWORD") }, "PASSWORD"},
		{"no database", base, func(e map[string]string) { delete(e, "AEGIS_DATABASE_URL") }, "-database-url"},
		{"stray argument", append(append([]string{}, base...), "extra"), nil, "unexpected arguments"},
	}
	for _, tc := range cases {
		env := smokeEnv()
		if tc.env != nil {
			tc.env(env)
		}
		_, err := parseOptions(tc.args, fakeEnv(env), testTenant, io.Discard)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want mention of %q", tc.name, err, tc.want)
		}
	}
}

func TestValidBase(t *testing.T) {
	for raw, want := range map[string]bool{
		"http://127.0.0.1:9003": true,
		"https://node.test":     true,
		"https://node.test/":    true,
		"https://node.test/x":   false,
		"ftp://node.test":       false,
		"https://u:p@node.test": false,
		"https://node.test?x=1": false,
		"127.0.0.1:9003":        false,
		"":                      false,
	} {
		if got := validBase(raw, false); got != want {
			t.Fatalf("validBase(%q,false) = %v, want %v", raw, got, want)
		}
	}
	if !validBase("https://panel.test/entry", true) {
		t.Fatal("admin base may carry the admin entry prefix")
	}
}
