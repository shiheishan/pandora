// Command aegis-payctl 配置支付渠道。
//
// 渠道凭据（易支付的商户号与密钥）必须信封加密后入库（SEC-010），
// 而密文的 AAD 绑定渠道行 ID，所以「插入行」与「加密凭据」有先后依赖，
// 纯 SQL 脚本做不到。管理端 UI 落地前，由这个工具承担。
//
// 用法：
//
//	aegis-payctl upsert-epay \
//	  --tenant <tenant-id> --code epay --name "易支付" \
//	  --base-url https://pay.example.com \
//	  --merchant 1001 --key <商户密钥> \
//	  [--default-method alipay] [--enable] [--allow-private-host]
//
//	aegis-payctl list --tenant <tenant-id>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/payment"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
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
		merchant      = fs.String("merchant", "", "商户号 pid（必填）")
		key           = fs.String("key", "", "商户密钥（必填）")
		submitPath    = fs.String("submit-path", "/submit.php", "下单路径")
		apiPath       = fs.String("api-path", "/api.php", "查询接口路径")
		defaultMethod = fs.String("default-method", "alipay", "默认支付方式")
		enable        = fs.Bool("enable", false, "是否启用")
		allowPrivate  = fs.Bool("allow-private-host", false, "允许内网地址（仅开发环境）")
	)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	for n, v := range map[string]string{
		"tenant": *tenant, "base-url": *baseURL, "merchant": *merchant, "key": *key,
	} {
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

	providerConfig, _ := json.Marshal(map[string]any{
		"base_url":           *baseURL,
		"submit_path":        *submitPath,
		"api_path":           *apiPath,
		"default_method":     *defaultMethod,
		"allow_private_host": *allowPrivate,
	})

	creds, err := payment.EncodeCredentials(payment.Credentials{
		MerchantID: *merchant,
		Key:        *key,
	})
	if err != nil {
		return err
	}

	var providerID string
	err = pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		// 先落行拿到 ID —— 凭据密文的 AAD 要绑定它
		err := tx.QueryRow(ctx, `
			INSERT INTO payment_providers
				(tenant_id, code, adapter, display_name, supported_currencies,
				 config, enabled, accepting_new)
			VALUES ($1, $2, 'epay', $3, ARRAY['CNY']::app.currency_code[], $4, $5, true)
			ON CONFLICT (tenant_id, code) DO UPDATE
			   SET display_name = EXCLUDED.display_name,
			       config       = EXCLUDED.config,
			       enabled      = EXCLUDED.enabled
			RETURNING id`,
			*tenant, *code, *name, providerConfig, *enable,
		).Scan(&providerID)
		if err != nil {
			return err
		}

		// AAD 绑定渠道 ID：把密文搬到另一条渠道记录上会解不开
		sealed, err := env.Seal(creds, []byte("payment_provider:"+providerID))
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx,
			`UPDATE payment_providers SET credentials_encrypted = $1, key_version = 1
			  WHERE id = $2`, sealed, providerID)
		return err
	})
	if err != nil {
		return err
	}

	fmt.Printf("已配置易支付渠道\n  provider_id = %s\n  code        = %s\n  base_url    = %s\n  merchant    = %s\n  enabled     = %v\n",
		providerID, *code, *baseURL, *merchant, *enable)
	fmt.Println("  凭据已信封加密入库，明文不落库、不落日志")
	return nil
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
