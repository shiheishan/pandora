// [INPUT]: 依赖同包 options.go（参数）、naming.go（命名空间与地址）、retire.go、nodes.go、enroll.go、users.go、verify.go，
//          依赖 platform/db 的 Pool（直接包一层 pgxpool，不走 db.Open 的运行角色检查，迁移账号也能用）、platform/crypto 的口令哈希与信封、middleware 的默认租户、ltkit 的 Manifest
// [OUTPUT]: 对外提供 Main（go run ./tools/loadtest seed ...）
// [POS]: tools/loadtest 的 seed 子命令：在已迁移的库里造一档压测数据并写 manifest。阶段顺序是 退役旧批次 → 池与套餐草稿 → 服务器 → 节点与接入令牌 →
//        节点接入 → 一步上线 → 发布套餐 → 用户与订阅 → 核对，每段计时打印到 stdout 并进 manifest.SeedTimings
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// Main 是 seed 子命令的入口。
func Main(args []string) error {
	o, err := parseOptions(args, os.Getenv, middleware.DefaultTenantID, os.Stderr)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return run(ctx, o, os.Stdout)
}

// timings 记阶段耗时，同时打印一行。
type timings struct {
	out  io.Writer
	list []ltkit.SeedTiming
}

func (t *timings) add(phase string, count int, d time.Duration) {
	t.list = append(t.list, ltkit.SeedTiming{Phase: phase, Count: count, Seconds: d.Round(time.Millisecond).Seconds()})
	fmt.Fprintf(t.out, "    %-18s %7d  %9.3fs\n", phase, count, d.Seconds())
}

// track 跑一段并计时；count 在段内才知道时由 fn 返回。
func (t *timings) track(phase string, fn func() (int, error)) error {
	start := time.Now()
	n, err := fn()
	if err != nil {
		return fmt.Errorf("%s: %w", phase, err)
	}
	t.add(phase, n, time.Since(start))
	return nil
}

func run(ctx context.Context, o *options, out io.Writer) error {
	begin := time.Now()
	runID, err := newRunID()
	if err != nil {
		return err
	}
	ns := newNamespace(o.Label, runID)
	fmt.Fprintf(out, "==> loadtest seed: label=%s run=%s users=%d nodes=%d (%d per server)\n",
		o.Label, runID, o.Users, o.Nodes, o.NodesPerServer)

	pool, err := openPool(ctx, o.DatabaseURL, out)
	if err != nil {
		return err
	}
	defer pool.Close()

	var envelope *crypto.Envelope
	if o.MasterKey != "" {
		key, err := base64.StdEncoding.DecodeString(o.MasterKey)
		if err != nil {
			return fmt.Errorf("AEGIS_MASTER_KEY is not valid base64")
		}
		if envelope, err = crypto.NewEnvelope(key); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, "    AEGIS_MASTER_KEY not set: subscription tokens are stored hash-only, the portal link list stays empty")
	}

	admin := newAdminClient(o.AdminBase, o.AdminEmail, o.AdminPassword, o.AdminInterval)
	nc := newNodeClient(o.NodeBase, o.AgentVersion, o.BinarySHA256)
	tm := &timings{out: out}
	m := &ltkit.Manifest{CreatedAt: time.Now().UTC(), Label: o.Label, TenantID: o.TenantID, RunID: runID}

	fmt.Fprintf(out, "    %-18s %7s  %10s\n", "phase", "count", "seconds")
	if err := tm.track("admin_login", func() (int, error) { return 1, admin.login(ctx) }); err != nil {
		return err
	}
	if o.RetirePrevious {
		if err := tm.track("retire_previous", func() (int, error) {
			r, err := retirePrevious(ctx, admin, pool, o.TenantID)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(out, "    retired %d earlier load-test nodes, expired %d earlier load-test subscriptions\n", r.Nodes, r.Subscriptions)
			return r.Nodes + r.Subscriptions, nil
		}); err != nil {
			return err
		}
	}

	var planID, versionID string
	if err := tm.track("catalog", func() (int, error) {
		var err error
		m.PoolID, planID, versionID, err = createCatalog(ctx, admin, ns, o)
		return 2, err
	}); err != nil {
		return err
	}
	m.PlanIDs = []string{planID}

	var serverIDs []string
	if err := tm.track("servers", func() (int, error) {
		var err error
		serverIDs, err = createServers(ctx, admin, ns, serverCount(o.Nodes, o.NodesPerServer), o.NodesPerServer)
		return len(serverIDs), err
	}); err != nil {
		return err
	}

	var nodes []*seededNode
	if err := tm.track("nodes_create", func() (int, error) {
		var err error
		nodes, err = createNodes(ctx, admin, ns, m.PoolID, serverIDs, o.Nodes, o.NodesPerServer)
		return len(nodes), err
	}); err != nil {
		return err
	}
	if err := tm.track("nodes_enroll", func() (int, error) {
		return len(nodes), enrollNodes(ctx, nc, nodes, o.EnrollWorkers)
	}); err != nil {
		return err
	}
	if err := tm.track("nodes_activate", func() (int, error) {
		return len(nodes), activateNodes(ctx, admin, pool, o.TenantID, nodes)
	}); err != nil {
		return err
	}
	if err := tm.track("plan_publish", func() (int, error) { return 1, publishPlan(ctx, admin, planID, versionID) }); err != nil {
		return err
	}
	for _, n := range nodes {
		m.Nodes = append(m.Nodes, ltkit.ManifestNode{
			ID: n.ID, NodeType: seedNodeType, RuntimeToken: n.Identity.RuntimeToken,
			PrivateKey: base64.StdEncoding.EncodeToString(n.Identity.PrivateKey), Serial: n.Enroll.Serial,
			ConfigKeyID: n.Enroll.ConfigKeyID, ConfigPublicKey: n.Enroll.ConfigPublicKey,
			Name: n.Name, ServerID: n.ServerID,
		})
	}

	// 用户段：口令全批只哈希一次（Argon2id 一次约几十毫秒，逐个算 15k 次要十几分钟）
	terms, err := loadPlanTerms(ctx, pool, o.TenantID, planID, versionID, time.Now().UTC())
	if err != nil {
		return err
	}
	m.PlanVersionID = terms.VersionID
	if m.SubscribePathPrefix, err = loadSubscribePrefix(ctx, pool, o.TenantID); err != nil {
		return fmt.Errorf("read subscription path prefix: %w", err)
	}
	if m.UserPassword, err = newUserPassword(); err != nil {
		return err
	}
	var phc string
	if err := tm.track("password_hash", func() (int, error) {
		var err error
		phc, err = crypto.HashPassword(m.UserPassword, crypto.DefaultArgon2Params())
		return 1, err
	}); err != nil {
		return err
	}
	usersStart := time.Now()
	res, err := seedUsers(ctx, pool, userSeedInput{
		TenantID: o.TenantID, NS: ns, Terms: terms, PHC: phc, Users: o.Users, BatchSize: o.Batch, Envelope: envelope,
	}, func(done int) {
		if done%(o.Batch*5) == 0 || done == o.Users {
			fmt.Fprintf(out, "    ... %d/%d users\n", done, o.Users)
		}
	})
	if err != nil {
		return fmt.Errorf("users: %w", err)
	}
	tm.add("users", len(res.Users), res.UsersTime)
	tm.add("subscriptions", res.Subscriptions, res.SubsTime)
	tm.add("users_total", len(res.Users), time.Since(usersStart))
	m.Users = res.Users
	if err := writeSeedAudit(ctx, pool, o.TenantID, ns, o.Users, o.Nodes); err != nil {
		return fmt.Errorf("audit: %w", err)
	}

	if o.Verify {
		if err := tm.track("verify", func() (int, error) {
			rep, err := verifySeed(ctx, nc, nodes, m.Users, o.PublicBase, m.SubscribePathPrefix)
			if err != nil {
				return 0, err
			}
			fmt.Fprintf(out, "    verify: %d nodes pass signed effective-config and UniProxy config, each lists exactly %d seeded users; subscription pull ok=%v\n",
				rep.NodesChecked, rep.UsersPerNode, rep.SubscribeOK)
			return rep.NodesChecked, nil
		}); err != nil {
			return err
		}
	}
	tm.add("total", o.Users+o.Nodes, time.Since(begin))
	fmt.Fprintf(out, "    admin gateway requests: %d (paced at %s)\n", admin.Calls, o.AdminInterval)
	m.SeedTimings = tm.list
	if err := m.Save(o.Out); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	fmt.Fprintf(out, "==> manifest written: %s (run %s, %d users, %d nodes)\n", o.Out, runID, len(m.Users), len(m.Nodes))
	return nil
}

// openPool 直接开一个 pgxpool 再包成 db.Pool：InTx 的租户上下文照常注入。
// 不走 db.Open，是因为它拒绝超级用户与 BYPASSRLS 角色——冒烟栈的迁移账号就是超级用户；
// 推荐用运行角色 aegis_app（RLS 与列级授权都是真的），用了能绕过 RLS 的角色时打一行提示。
func openPool(ctx context.Context, dsn string, out io.Writer) (*db.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = 4
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	var role string
	var bypass bool
	if err := p.QueryRow(ctx, `SELECT current_user, rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&role, &bypass); err != nil {
		p.Close()
		return nil, fmt.Errorf("database unreachable: %w", err)
	}
	if bypass {
		fmt.Fprintf(out, "    note: database role %q bypasses RLS; prefer the aegis_app runtime role so policies are exercised\n", role)
	}
	return &db.Pool{Pool: p}, nil
}
