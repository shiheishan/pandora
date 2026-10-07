// Command aegis-payctl 配置支付渠道。
//
// 渠道凭据（易支付的商户号与密钥）必须信封加密后入库（SEC-010），
// 而密文的 AAD 绑定渠道行 ID，所以「插入行」与「加密凭据」有先后依赖，
// 纯 SQL 脚本做不到。后台「支付渠道」页也能建与改；两边调的是 billing 里
// 同一份写入（CreateProvider / UpdateProvider），口径一致、都写审计。
// 已存在的渠道按更新处理：商户号与密钥留空表示沿用库里的那一份。
//
// 用法：
//
//	aegis-payctl upsert-epay \
//	  --tenant <tenant-id> --code epay --name "易支付" \
//	  --base-url https://pay.example.com \
//	  --merchant 1001 --key <商户密钥> \
//	  [--methods alipay,wxpay] [--default-method alipay] [--enable] [--allow-private-host]
//
//	aegis-payctl list --tenant <tenant-id>
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("用法: aegis-payctl <upsert-epay|list> [flags]")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "upsert-epay":
		return upsertEpay(ctx, pool, cfg)
	case "list":
		return list(ctx, pool)
	default:
		return fmt.Errorf("未知子命令 %q", os.Args[1])
	}
}

func upsertEpay(ctx context.Context, pool *db.Pool, cfg *config.Config) error {
	fs := flag.NewFlagSet("upsert-epay", flag.ExitOnError)
	var (
		tenant        = fs.String("tenant", "", "租户 ID（必填）")
		code          = fs.String("code", "epay", "渠道编码")
		name          = fs.String("name", "易支付", "展示名")
		baseURL       = fs.String("base-url", "", "易支付站点地址，如 https://pay.example.com（必填）")
		merchant      = fs.String("merchant", "", "商户号 pid（新建必填；更新时留空 = 不改）")
		key           = fs.String("key", "", "商户密钥（新建必填；更新时留空 = 不改）")
		submitPath    = fs.String("submit-path", "/submit.php", "下单路径")
		apiPath       = fs.String("api-path", "/api.php", "查询接口路径")
		methods       = fs.String("methods", "alipay", "支付方式，逗号分隔，取值 alipay / wxpay / qqpay")
		defaultMethod = fs.String("default-method", "", "默认支付方式（留空取 --methods 的第一个）")
		enable        = fs.Bool("enable", false, "是否启用")
		allowPrivate  = fs.Bool("allow-private-host", false, "允许内网地址（仅开发环境）")
	)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	for n, v := range map[string]string{"tenant": *tenant, "base-url": *baseURL} {
		if v == "" {
			return fmt.Errorf("--%s 必填", n)
		}
	}

	if *allowPrivate && cfg.IsProduction() {
		return errors.New("生产环境不允许 --allow-private-host（SEC-007）")
	}

	env, err := crypto.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return err
	}
	// 与后台同一份写入：先落行、再按 id 作 AAD 加密，保存前用适配器试构造，并写审计。
	// 只用到渠道写入，不需要结算服务。
	svc := billing.NewPaymentService(nil, pool, env, cfg.MasterKey, cfg.PublicBaseURL, !cfg.IsProduction())
	settings := billing.ProviderSettings{
		DisplayName: *name, BaseURL: *baseURL, SubmitPath: *submitPath, APIPath: *apiPath,
		Methods: splitMethods(*methods), DefaultMethod: *defaultMethod,
		AllowPrivateHost: *allowPrivate, MerchantID: *merchant, Key: *key,
	}
	actor := billing.ProviderActor{Kind: "system"}

	action := "已更新"
	out, err := svc.UpdateProvider(ctx, *tenant, actor, billing.UpdateProviderInput{
		Code: *code, ProviderSettings: settings, Enabled: enable,
	})
	if httpErr := new(httpx.Error); errors.As(err, &httpErr) && httpErr.Code == httpx.CodeNotFound {
		action = "已新建"
		out, err = svc.CreateProvider(ctx, *tenant, actor, billing.CreateProviderInput{
			Code: *code, Adapter: "epay", ProviderSettings: settings,
			Enabled: *enable, AcceptingNew: true,
		})
	}
	if err != nil {
		return describeError(err)
	}

	fmt.Printf("%s易支付渠道\n  provider_id = %s\n  code        = %s\n  base_url    = %s\n  methods     = %s\n  enabled     = %v\n  凭据变更    = %v\n",
		action, out.ID, out.Code, *baseURL, *methods, *enable, out.CredentialsChanged)
	fmt.Println("  凭据已信封加密入库，明文不落库、不落日志")
	return nil
}

func splitMethods(raw string) []string {
	var out []string
	for _, m := range strings.Split(raw, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// describeError 把校验错误的逐字段原因摊开：命令行没有表单可以标红。
func describeError(err error) error {
	httpErr := new(httpx.Error)
	if !errors.As(err, &httpErr) {
		return err
	}
	if len(httpErr.Fields) == 0 {
		return errors.New(httpErr.Message)
	}
	keys := make([]string, 0, len(httpErr.Fields))
	for k := range httpErr.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+httpErr.Fields[k])
	}
	return fmt.Errorf("%s（%s）", httpErr.Message, strings.Join(parts, "；"))
}

func list(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	tenant := fs.String("tenant", "", "租户 ID（必填）")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *tenant == "" {
		return errors.New("--tenant 必填")
	}

	return pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT code, adapter, display_name, enabled, accepting_new,
			       (credentials_encrypted IS NOT NULL) AS has_creds,
			       coalesce(config->>'base_url', '')
			  FROM payment_providers WHERE tenant_id = $1 ORDER BY code`, *tenant)
		if err != nil {
			return err
		}
		defer rows.Close()

		fmt.Printf("%-12s %-8s %-16s %-8s %-10s %-8s %s\n",
			"CODE", "ADAPTER", "NAME", "ENABLED", "ACCEPTING", "CREDS", "BASE_URL")
		for rows.Next() {
			var code, adapter, name, baseURL string
			var enabled, accepting, hasCreds bool
			if err := rows.Scan(&code, &adapter, &name, &enabled, &accepting, &hasCreds, &baseURL); err != nil {
				return err
			}
			fmt.Printf("%-12s %-8s %-16s %-8v %-10v %-8v %s\n",
				code, adapter, name, enabled, accepting, hasCreds, baseURL)
		}
		return rows.Err()
	})
}
