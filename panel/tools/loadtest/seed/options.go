// [INPUT]: 依赖 flag、regexp 与调用方传入的环境变量读取函数（Main 传 os.Getenv，测试传假表）
// [OUTPUT]: 包内提供 options、parseOptions、firstNonEmpty、validBase 与各项默认值常量
// [POS]: tools/loadtest/seed 的命令行：flag 优先、环境变量兜底（LOADTEST_* → 冒烟栈 smoke.env 的 SMOKE_* → 面板自己的 AEGIS_*），口令只从环境变量读、绝不进 argv；纯函数，单测钉住优先级与校验
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	defaultNodes         = 200
	defaultNodesPer      = 1
	defaultBatch         = 1000
	defaultTrafficGB     = 1024
	defaultMaxDevices    = 3
	defaultEnrollWorkers = 8
	// 后台网关按来源 IP 每分钟 240 次限流（api/admin/router.go 写死），260ms 一次约 230 次/分
	defaultAdminInterval = 260 * time.Millisecond
	// 节点接入令牌最长 30 分钟（NODE-008），建完全部节点再统一接入，节点太多会等到令牌过期
	maxNodes = 1500
)

type options struct {
	DatabaseURL string
	AdminBase   string
	NodeBase    string
	// PublicBase 可空：给了才在核对阶段真拉一次订阅
	PublicBase    string
	AdminEmail    string
	AdminPassword string
	// MasterKey 可空：给了就像面板一样把订阅令牌信封加密存一份，门户「我的订阅链接」才看得见
	MasterKey string

	TenantID       string
	Users          int
	Nodes          int
	NodesPerServer int
	Label          string
	Out            string
	Batch          int
	TrafficGB      int
	MaxDevices     int
	EnrollWorkers  int
	AdminInterval  time.Duration
	RetirePrevious bool
	Verify         bool

	// 接入提交上报的发布物证据：生产口径下必须与面板钉住的 NativeCore 产物一致
	AgentVersion string
	BinarySHA256 string
}

var labelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,14}[a-z0-9])?$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// parseOptions 解析 seed 的参数。getenv 只用来补 flag 没给的值。
func parseOptions(args []string, getenv func(string) string, tenantDefault string, stderr io.Writer) (*options, error) {
	o := &options{}
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.DatabaseURL, "database-url", "", "PostgreSQL DSN; falls back to LOADTEST_DATABASE_URL, then AEGIS_DATABASE_URL (prefer the aegis_app role so RLS applies)")
	fs.StringVar(&o.AdminBase, "admin-base", "", "admin gateway origin; falls back to LOADTEST_ADMIN_BASE, then SMOKE_ADMIN_BASE")
	fs.StringVar(&o.NodeBase, "node-base", "", "node gateway origin (served at its root); falls back to LOADTEST_NODE_BASE, then SMOKE_NODE_BASE")
	fs.StringVar(&o.PublicBase, "public-base", "", "optional public gateway origin for one subscription pull during verify; falls back to LOADTEST_PUBLIC_BASE, then SMOKE_PUBLIC_BASE")
	fs.StringVar(&o.AdminEmail, "admin-email", "", "admin account email; falls back to LOADTEST_ADMIN_EMAIL, then SMOKE_ADMIN_EMAIL. The password is read only from LOADTEST_ADMIN_PASSWORD or SMOKE_ADMIN_PASSWORD")
	fs.StringVar(&o.TenantID, "tenant-id", tenantDefault, "tenant UUID")
	fs.IntVar(&o.Users, "users", 0, "number of fictitious users, each with one active subscription (required)")
	fs.IntVar(&o.Nodes, "nodes", defaultNodes, "number of nodes to create, enroll and activate")
	fs.IntVar(&o.NodesPerServer, "nodes-per-server", defaultNodesPer, "nodes placed on each created server")
	fs.StringVar(&o.Label, "label", "", "tier label such as 5k, 10k, 15k or ci: lowercase letters, digits and hyphens, at most 16 (required)")
	fs.StringVar(&o.Out, "out", "", "manifest output path, written 0600; keep it outside the repository (required)")
	fs.IntVar(&o.Batch, "batch", defaultBatch, "users inserted per transaction")
	fs.IntVar(&o.TrafficGB, "traffic-gb", defaultTrafficGB, "traffic quota of the load-test plan in GB")
	fs.IntVar(&o.MaxDevices, "max-devices", defaultMaxDevices, "device limit of the load-test plan")
	fs.IntVar(&o.EnrollWorkers, "enroll-workers", defaultEnrollWorkers, "concurrent node enrollments against the node gateway")
	fs.DurationVar(&o.AdminInterval, "admin-interval", defaultAdminInterval, "minimum spacing between admin gateway requests (the gateway allows 240 per minute per IP)")
	fs.BoolVar(&o.RetirePrevious, "retire-previous", true, "retire nodes and expire subscriptions left by earlier seeds (loadtest- names, @"+loadtestEmailDomain+" users) before creating the new batch")
	fs.BoolVar(&o.Verify, "verify", true, "after seeding, check signed effective-config and the UniProxy user list of the first and last node")
	fs.StringVar(&o.AgentVersion, "agent-version", "", "agent_version reported at enrollment commit; falls back to PANDORA_NATIVE_RELEASE_VERSION, then \"loadtest\"")
	fs.StringVar(&o.BinarySHA256, "binary-sha256", "", "amd64 binary digest reported at enrollment commit; falls back to PANDORA_NATIVE_ARTIFACT_AMD64_SHA256, then a placeholder digest")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}

	o.DatabaseURL = firstNonEmpty(o.DatabaseURL, getenv("LOADTEST_DATABASE_URL"), getenv("AEGIS_DATABASE_URL"))
	o.AdminBase = firstNonEmpty(o.AdminBase, getenv("LOADTEST_ADMIN_BASE"), getenv("SMOKE_ADMIN_BASE"))
	o.NodeBase = firstNonEmpty(o.NodeBase, getenv("LOADTEST_NODE_BASE"), getenv("SMOKE_NODE_BASE"))
	o.PublicBase = firstNonEmpty(o.PublicBase, getenv("LOADTEST_PUBLIC_BASE"), getenv("SMOKE_PUBLIC_BASE"))
	o.AdminEmail = firstNonEmpty(o.AdminEmail, getenv("LOADTEST_ADMIN_EMAIL"), getenv("SMOKE_ADMIN_EMAIL"))
	o.AdminPassword = firstNonEmpty(getenv("LOADTEST_ADMIN_PASSWORD"), getenv("SMOKE_ADMIN_PASSWORD"))
	o.MasterKey = strings.TrimSpace(getenv("AEGIS_MASTER_KEY"))
	o.AgentVersion = firstNonEmpty(o.AgentVersion, getenv("PANDORA_NATIVE_RELEASE_VERSION"), "loadtest")
	o.BinarySHA256 = strings.ToLower(firstNonEmpty(o.BinarySHA256, getenv("PANDORA_NATIVE_ARTIFACT_AMD64_SHA256"), strings.Repeat("1", 64)))
	for _, p := range []*string{&o.AdminBase, &o.NodeBase, &o.PublicBase} {
		*p = strings.TrimRight(*p, "/")
	}

	var problems []string
	need := func(ok bool, msg string) {
		if !ok {
			problems = append(problems, msg)
		}
	}
	need(o.DatabaseURL != "", "-database-url (or LOADTEST_DATABASE_URL / AEGIS_DATABASE_URL) is required")
	need(o.AdminEmail != "", "-admin-email (or LOADTEST_ADMIN_EMAIL / SMOKE_ADMIN_EMAIL) is required")
	need(o.AdminPassword != "", "LOADTEST_ADMIN_PASSWORD or SMOKE_ADMIN_PASSWORD must be set")
	need(validBase(o.AdminBase, true), "-admin-base must be an http(s) URL without query (a path prefix such as the admin entry is allowed)")
	need(validBase(o.NodeBase, false), "-node-base must be an http(s) origin without path")
	need(o.PublicBase == "" || validBase(o.PublicBase, false), "-public-base must be an http(s) origin without path when given")
	need(o.TenantID != "", "-tenant-id is required")
	need(o.Users > 0 && o.Users <= maxUsers, fmt.Sprintf("-users must be 1..%d", maxUsers))
	need(o.Nodes > 0 && o.Nodes <= maxNodes, fmt.Sprintf("-nodes must be 1..%d", maxNodes))
	need(o.NodesPerServer > 0, "-nodes-per-server must be positive")
	need(labelPattern.MatchString(o.Label), "-label must be 1-16 lowercase letters, digits or inner hyphens")
	need(o.Out != "", "-out is required")
	need(o.Batch > 0 && o.Batch <= 10000, "-batch must be 1..10000")
	need(o.TrafficGB > 0, "-traffic-gb must be positive")
	need(o.MaxDevices > 0, "-max-devices must be positive")
	need(o.EnrollWorkers > 0 && o.EnrollWorkers <= 64, "-enroll-workers must be 1..64")
	need(o.AdminInterval >= 0, "-admin-interval must not be negative")
	need(sha256HexPattern.MatchString(o.BinarySHA256), "-binary-sha256 must be 64 lowercase hex characters")
	if len(problems) > 0 {
		return nil, errors.New(strings.Join(problems, "; "))
	}
	return o, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

// validBase 校验网关地址。节点网关与公共网关只认 scheme + host[:port]：节点签名覆盖的是网关
// 看到的路径，订阅路径前缀也挂在根上，多一段路径就对不上；后台可以挂在反代的入口前缀下。
func validBase(raw string, allowPath bool) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		(allowPath || u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && u.User == nil
}
