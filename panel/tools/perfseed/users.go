package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	pcrypto "github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	nodesvc "github.com/aegispanel/aegis/internal/module/node/service"
	"golang.org/x/crypto/argon2"
)

const tokenBytes = 32 // 与 subscription/service/internal.go 的 tokenBytes 一致

// hashPasswordPHC 与 identity/service/password.go 的 hashPHC 一致（defaultArgon）；那边改参数要同步。
func hashPasswordPHC(password string) (string, error) {
	const memory = 19 * 1024
	const iterations = 2
	const parallelism = 1
	const saltLength = 16
	const keyLength = 32

	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐值: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memory, iterations, parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func runUsers(ctx context.Context, o usersOpts) error {
	start := time.Now()

	if err := os.MkdirAll(o.OutDir, 0o700); err != nil {
		return fmt.Errorf("创建输出目录: %w", err)
	}

	password, err := readPasswordFile(o.UserPasswordFile)
	if err != nil {
		return err
	}
	dsn, err := readDSNFile(o.DSNFile)
	if err != nil {
		return err
	}
	masterKey, err := readEnvValue(o.SecretsEnv, "PANDORA_SECRETS_MASTER_KEY")
	if err != nil {
		return err
	}
	redisURL, err := readEnvValue(o.SecretsEnv, "PANDORA_REDIS_URL")
	if err != nil {
		return err
	}

	catalogPath := filepath.Join(o.OutDir, fileCatalog)
	var catalog Catalog
	ok, err := loadJSON(catalogPath, &catalog)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("先跑 perfseed activate")
	}

	infraPath := filepath.Join(o.OutDir, fileInfra)
	var infra InfraState
	ok, err = loadJSON(infraPath, &infra)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("先跑 perfseed infra")
	}
	if infra.GroupID == "" {
		return errors.New("先跑 perfseed infra")
	}

	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("连接数据库失败: %w", err)
	}
	defer conn.Close(ctx)

	emailLike := userEmailPrefix + "%" + userEmailDomain
	var existing int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM users WHERE email::text LIKE $1`, emailLike).Scan(&existing); err != nil {
		return fmt.Errorf("查询已有用户: %w", err)
	}
	csvPath := filepath.Join(o.OutDir, fileUsersCSV)
	if existing > 0 {
		if fileExists(csvPath) {
			fmt.Printf("users 已经做过（库里已有 %d 个 perfseed 用户，%s/users.csv 在），只推进名单纪元\n", existing, o.OutDir)
			return bumpUsersEpoch(ctx, redisURL)
		}
		return fmt.Errorf("库里已有 %d 个 perfseed 用户但 users.csv 不在：订阅令牌只在造数时写进 users.csv，换空面板重来", existing)
	}

	var orderCount int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&orderCount); err != nil {
		return fmt.Errorf("查询订单数: %w", err)
	}
	if orderCount != 0 {
		return errors.New("users 要求面板上还没有任何订单（空面板）：历史订单的付款时间在过去，已有订单会让付款通知的扫描游标回扫到它们")
	}

	var offlineProviderID string
	if err := conn.QueryRow(ctx, `SELECT id::text FROM payment_providers WHERE code = 'offline'`).Scan(&offlineProviderID); err != nil {
		return fmt.Errorf("查询 offline 支付渠道: %w", err)
	}

	var tzRaw *string
	// 没有这行时取 UTC（与 settings 的缺省一致），其他错误照常报
	err = conn.QueryRow(ctx, `SELECT value #>> '{}' FROM settings WHERE key = 'site.timezone'`).Scan(&tzRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("查询站点时区: %w", err)
	}
	tzName := "UTC"
	if tzRaw != nil && *tzRaw != "" {
		tzName = *tzRaw
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		return fmt.Errorf("解析站点时区 %q: %w", tzName, err)
	}

	plans, err := loadPlanSpecs(ctx, conn, catalog)
	if err != nil {
		return err
	}

	phc, err := hashPasswordPHC(password)
	if err != nil {
		return err
	}

	keys, err := pcrypto.NewKeys(masterKey)
	if err != nil {
		return fmt.Errorf("解析主密钥: %w", err)
	}
	sealer, err := keys.Sealer()
	if err != nil {
		return fmt.Errorf("创建封存器: %w", err)
	}
	issuer := func(subscriptionID string) (Credential, error) {
		b := make([]byte, tokenBytes)
		if _, err := rand.Read(b); err != nil {
			return Credential{}, fmt.Errorf("生成令牌: %w", err)
		}
		token := base64.RawURLEncoding.EncodeToString(b)
		sum := sha256.Sum256([]byte(token))
		sealed, err := sealer.Seal([]byte(token), subsvc.CredentialAAD(uuid.MustParse(subscriptionID)))
		if err != nil {
			return Credential{}, fmt.Errorf("封存令牌: %w", err)
		}
		return Credential{
			Token:  token,
			Hash:   sum[:],
			Prefix: token[:8],
			Sealed: sealed,
		}, nil
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	in := GenInput{
		Now:               now,
		Loc:               loc,
		Users:             o.Users,
		ExpiredCount:      o.Users * o.ExpiredPercent / 100,
		ExpireAt:          o.ExpireAt,
		ExpireCount:       o.ExpireCount,
		HistoryDays:       o.HistoryDays,
		Seed:              o.Seed,
		GroupID:           infra.GroupID,
		OfflineProviderID: offlineProviderID,
		Plans:             plans,
	}
	seed, err := Generate(in, issuer)
	if err != nil {
		return err
	}
	seed.PasswordPHC = phc

	var buf bytes.Buffer
	if err := writeUsersCSV(&buf, password, seed.CSV); err != nil {
		return err
	}
	tmpCSV := filepath.Join(o.OutDir, fileUsersCSV+".tmp")
	if err := writeFileAtomic(tmpCSV, buf.Bytes()); err != nil {
		return err
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		_ = os.Remove(tmpCSV)
		return fmt.Errorf("开启事务: %w", err)
	}
	if err := writeSeed(ctx, tx, seed, offlineProviderID); err != nil {
		_ = tx.Rollback(ctx)
		_ = os.Remove(tmpCSV)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = os.Remove(tmpCSV)
		return fmt.Errorf("提交事务: %w", err)
	}
	if err := os.Rename(tmpCSV, csvPath); err != nil {
		return fmt.Errorf("写出 users.csv: %w", err)
	}

	if err := bumpUsersEpoch(ctx, redisURL); err != nil {
		return err
	}

	expiredN := in.ExpiredCount
	var expireAtN int
	if o.ExpireAt != nil && o.ExpireCount > 0 {
		expireAtN = o.ExpireCount
	}
	fmt.Printf("汇总: 用户 %d，订阅 %d，订单 %d，按天用量 %d，审计 %d；已过期 %d", len(seed.Users), len(seed.Subscriptions), len(seed.Orders), len(seed.Usage), len(seed.Audits), expiredN)
	if expireAtN > 0 {
		fmt.Printf("，到期改到 ExpireAt %d", expireAtN)
	}
	fmt.Printf("；耗时 %s；名单纪元已推进\n", time.Since(start).Round(time.Millisecond))
	return nil
}

func loadPlanSpecs(ctx context.Context, conn *pgx.Conn, catalog Catalog) ([]PlanSpec, error) {
	var out []PlanSpec
	for _, cp := range catalog.Plans {
		var priceID string
		var interval string
		var intervalCount int
		found := false
		for _, p := range cp.Prices {
			if p.BillingInterval == "month" && p.IntervalCount == 1 {
				priceID = p.PriceID
				interval = p.BillingInterval
				intervalCount = p.IntervalCount
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("套餐 %s 没有月付×1 价格", cp.Code)
		}

		var name string
		var versionNo int
		if err := conn.QueryRow(ctx, `
			SELECT p.name, v.version FROM plans p
			JOIN plan_versions v ON v.plan_id = p.id
			WHERE p.id = $1 AND v.id = $2 AND v.status = 'published'`, cp.PlanID, cp.VersionID).Scan(&name, &versionNo); err != nil {
			return nil, fmt.Errorf("套餐 %s 版本: %w", cp.Code, err)
		}

		var amount string
		if err := conn.QueryRow(ctx, `
			SELECT unit_amount::text, billing_interval, interval_count
			FROM prices WHERE id = $1 AND plan_id = $2 AND status = 'active'`, priceID, cp.PlanID).
			Scan(&amount, &interval, &intervalCount); err != nil {
			return nil, fmt.Errorf("套餐 %s 价格: %w", cp.Code, err)
		}

		rows, err := conn.Query(ctx, `
			SELECT metric, limit_value, unit, period FROM quota_definitions
			WHERE plan_version_id = $1 ORDER BY metric, period`, cp.VersionID)
		if err != nil {
			return nil, fmt.Errorf("套餐 %s 配额: %w", cp.Code, err)
		}
		var quotas []QuotaDef
		for rows.Next() {
			var q QuotaDef
			var limit *int64
			if err := rows.Scan(&q.Metric, &limit, &q.Unit, &q.Period); err != nil {
				rows.Close()
				return nil, err
			}
			q.Limit = limit
			quotas = append(quotas, q)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}

		var entJSON, quotasJSON string
		if err := conn.QueryRow(ctx, `
			SELECT coalesce(jsonb_agg(jsonb_build_object('code', code, 'value', value) ORDER BY code), '[]'::jsonb)::text
			FROM entitlements WHERE plan_version_id = $1`, cp.VersionID).Scan(&entJSON); err != nil {
			return nil, fmt.Errorf("套餐 %s 权益: %w", cp.Code, err)
		}
		if err := conn.QueryRow(ctx, `
			SELECT coalesce(jsonb_agg(jsonb_build_object('metric', metric, 'limit', limit_value, 'unit', unit, 'period', period) ORDER BY metric, period), '[]'::jsonb)::text
			FROM quota_definitions WHERE plan_version_id = $1`, cp.VersionID).Scan(&quotasJSON); err != nil {
			return nil, fmt.Errorf("套餐 %s 配额 JSON: %w", cp.Code, err)
		}

		out = append(out, PlanSpec{
			PlanID:           cp.PlanID,
			VersionID:        cp.VersionID,
			PriceID:          priceID,
			Name:             name,
			VersionNo:        versionNo,
			Amount:           amount,
			Interval:         interval,
			IntervalCount:    intervalCount,
			Quotas:           quotas,
			EntitlementsJSON: entJSON,
			QuotasJSON:       quotasJSON,
		})
	}
	return out, nil
}

func bumpUsersEpoch(ctx context.Context, redisURL string) error {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("库已写好、users.csv 已写出，但名单纪元没推进（节点要等最多 15 秒的全量比对或重启 pandora-serve 才看到新用户）：解析 Redis 地址: %w", err)
	}
	client := redis.NewClient(opt)
	defer client.Close()

	pipe := client.Pipeline()
	pipe.Incr(ctx, nodesvc.UsersEpochKey)
	pipe.Publish(ctx, nodesvc.ChangedChannel, "users")
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("库已写好、users.csv 已写出，但名单纪元没推进（节点要等最多 15 秒的全量比对或重启 pandora-serve 才看到新用户）：%w", err)
	}
	return nil
}
