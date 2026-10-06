// Command aegis-adminctl 管理后台账号与角色。
//
// 存在的理由是「第一个管理员从哪来」：管理界面本身需要登录才能进，
// 而系统初装时一个管理员都没有。这类 bootstrap 只能从服务器本地完成，
// 不能开放成 HTTP 接口 —— 否则就是一个人人可调用的提权入口。
//
// 用法：
//
//	aegis-adminctl create --email admin@x.com --password-stdin [--role platform_admin]
//	aegis-adminctl reset-password --email admin@x.com --password-stdin
//	aegis-adminctl grant  --email admin@x.com --role finance
//	aegis-adminctl revoke --email admin@x.com --role finance
//	aegis-adminctl list
//	aegis-adminctl roles
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/credentialrevocation"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

const defaultTenant = middleware.DefaultTenantID

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("用法: aegis-adminctl <create|reset-password|grant|revoke|list|roles> [flags]")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch os.Args[1] {
	case "create":
		return create(ctx, pool)
	case "reset-password":
		return resetPassword(ctx, pool)
	case "grant":
		return bind(ctx, pool, true)
	case "revoke":
		return bind(ctx, pool, false)
	case "list":
		return list(ctx, pool)
	case "roles":
		return roles(ctx, pool)
	default:
		return fmt.Errorf("未知子命令 %q", os.Args[1])
	}
}

//------------------------------------------------------------------------------

func create(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	var (
		email         = fs.String("email", "", "管理员邮箱（必填）")
		passwordStdin = fs.Bool("password-stdin", false, "从标准输入读取密码，避免出现在进程参数中")
		name          = fs.String("name", "", "显示名")
		role          = fs.String("role", "platform_admin", "初始角色码")
		tenant        = fs.String("tenant", defaultTenant, "租户 ID")
	)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *email == "" || !*passwordStdin {
		return errors.New("--email 与 --password-stdin 必填")
	}
	password, err := readPasswordLine(os.Stdin)
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassword(password); err != nil {
		return err
	}

	phc, err := crypto.HashPassword(password, crypto.DefaultArgon2Params())
	if err != nil {
		return err
	}

	display := *name
	if display == "" {
		display = strings.SplitN(*email, "@", 2)[0]
	}

	var userID, roleID string
	err = pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		if err := iamguard.LockLastAdministrator(ctx, tx, *tenant); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT id FROM roles WHERE tenant_id = $1 AND code = $2`,
			*tenant, *role).Scan(&roleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("角色 %q 不存在，先用 aegis-adminctl roles 查看可用角色", *role)
			}
			return err
		}

		// 已存在则复用账号并改密：便于忘记密码时从服务器本地重置
		err := tx.QueryRow(ctx, `
			INSERT INTO users (tenant_id, email, display_name, status, email_verified_at, mfa_enforced)
			VALUES ($1, $2, $3, 'active', now(), true)
			ON CONFLICT (tenant_id, email) DO UPDATE
			   SET display_name = EXCLUDED.display_name, status = 'active'
			RETURNING id`,
			*tenant, *email, display).Scan(&userID)
		if err != nil {
			return err
		}

		revokedSessions, revokedRefresh, revokedFamilies, err := replacePasswordAndRevokeCredentials(
			ctx, tx, *tenant, userID, phc,
		)
		if err != nil {
			return err
		}

		if err := upsertTenantRoleBinding(ctx, tx, *tenant, userID, roleID,
			"由 aegis-adminctl 初始化", nil); err != nil {
			return err
		}
		if err := iamguard.RequireEffectiveAdministrator(ctx, tx, *tenant); err != nil {
			return err
		}
		return audit.Write(ctx, tx, *tenant, audit.Entry{
			ActorKind: "system", ActorLabel: "aegis-adminctl",
			Action: "adminctl.user_bootstrap", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			AfterDigest: map[string]any{
				"role": *role, "password_rotated": true,
				"sessions_revoked": revokedSessions, "refresh_tokens_revoked": revokedRefresh,
				"refresh_families_revoked": revokedFamilies,
			},
		})
	})
	if err != nil {
		return err
	}

	fmt.Printf("管理员已就绪\n  user_id = %s\n  email   = %s\n  role    = %s\n",
		userID, *email, *role)
	fmt.Println("  提示：该账号已标记 mfa_enforced，正式对外前应完成 MFA 绑定（IAM-004）")
	return nil
}

// resetPassword narrowly rotates an existing account credential. Unlike create,
// it cannot reactivate the account, overwrite its display name, or grant a role.
// The new password is accepted only on stdin so it cannot leak through process
// listings, shell history, or operator transcripts.
func resetPassword(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	email := fs.String("email", "", "管理员邮箱（必填）")
	passwordStdin := fs.Bool("password-stdin", false, "从标准输入读取新密码（必填）")
	tenant := fs.String("tenant", defaultTenant, "租户 ID")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *email == "" || !*passwordStdin {
		return errors.New("--email 与 --password-stdin 必填")
	}
	password, err := readPasswordLine(os.Stdin)
	if err != nil {
		return err
	}
	if err := crypto.ValidatePassword(password); err != nil {
		return err
	}
	phc, err := crypto.HashPassword(password, crypto.DefaultArgon2Params())
	if err != nil {
		return err
	}

	var userID string
	err = pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		if err := iamguard.LockLastAdministrator(ctx, tx, *tenant); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			SELECT id
			  FROM users
			 WHERE tenant_id = $1 AND email = $2
			 FOR UPDATE`, *tenant, strings.TrimSpace(*email)).Scan(&userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("管理员账号 %q 不存在", *email)
			}
			return err
		}
		if err := iamguard.RequireUserEffectiveAdministrator(ctx, tx, *tenant, userID); err != nil {
			return err
		}

		revokedSessions, revokedRefresh, revokedFamilies, err := replacePasswordAndRevokeCredentials(
			ctx, tx, *tenant, userID, phc,
		)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, *tenant, audit.Entry{
			ActorKind: "system", ActorLabel: "aegis-adminctl",
			Action: "adminctl.password_reset", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			AfterDigest: map[string]any{
				"password_rotated": true, "sessions_revoked": revokedSessions,
				"refresh_tokens_revoked":   revokedRefresh,
				"refresh_families_revoked": revokedFamilies,
			},
		})
	})
	if err != nil {
		return err
	}

	fmt.Printf("管理员密码已重置，全部既有会话已失效\n  user_id = %s\n  email   = %s\n", userID, *email)
	return nil
}

func readPasswordLine(r io.Reader) (string, error) {
	value, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", fmt.Errorf("从标准输入读取密码: %w", err)
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if value == "" {
		return "", errors.New("标准输入中的密码不能为空")
	}
	return value, nil
}

func replacePasswordAndRevokeCredentials(
	ctx context.Context, tx pgx.Tx, tenantID, userID, phc string,
) (int64, int64, int64, error) {
	passwordTag, err := tx.Exec(ctx, `
		INSERT INTO user_passwords (user_id, tenant_id, phc)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		   SET phc = EXCLUDED.phc, rotated_at = now(), must_rotate = false`,
		userID, tenantID, phc)
	if err != nil {
		return 0, 0, 0, err
	}
	if passwordTag.RowsAffected() != 1 {
		return 0, 0, 0, fmt.Errorf("password rotation affected %d rows", passwordTag.RowsAffected())
	}

	sessionTag, err := tx.Exec(ctx, `
		UPDATE sessions
		   SET revoked_at = now(), revoked_reason = 'password_changed'
		 WHERE tenant_id = $1 AND user_id = $2::uuid AND revoked_at IS NULL`,
		tenantID, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	refreshTag, err := tx.Exec(ctx, `
		UPDATE refresh_tokens
		   SET status = 'revoked'
		 WHERE tenant_id = $1 AND user_id = $2::uuid AND status = 'active'`,
		tenantID, userID)
	if err != nil {
		return 0, 0, 0, err
	}
	refreshFamilies, err := credentialrevocation.RevokeRefreshFamilies(
		ctx, tx, tenantID, userID,
	)
	if err != nil {
		return 0, 0, 0, err
	}
	return sessionTag.RowsAffected(), refreshTag.RowsAffected(), refreshFamilies, nil
}

func bind(ctx context.Context, pool *db.Pool, grant bool) error {
	name := "grant"
	if !grant {
		name = "revoke"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	var (
		email  = fs.String("email", "", "管理员邮箱（必填）")
		role   = fs.String("role", "", "角色码（必填）")
		tenant = fs.String("tenant", defaultTenant, "租户 ID")
		hours  = fs.Int("hours", 0, "仅 grant：临时提权小时数，0 表示长期（IAM-010）")
	)
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *email == "" || *role == "" {
		return errors.New("--email 与 --role 必填")
	}

	err := pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		if err := iamguard.LockLastAdministrator(ctx, tx, *tenant); err != nil {
			return err
		}
		var userID, roleID string
		if err := tx.QueryRow(ctx,
			`SELECT id FROM users WHERE tenant_id = $1 AND email = $2`,
			*tenant, *email).Scan(&userID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("用户 %q 不存在", *email)
			}
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT id FROM roles WHERE tenant_id = $1 AND code = $2`,
			*tenant, *role).Scan(&roleID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("角色 %q 不存在", *role)
			}
			return err
		}

		if !grant {
			ct, err := tx.Exec(ctx, `
				DELETE FROM role_bindings
				 WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3
				   AND scope_type = 'tenant' AND scope_id IS NULL`,
				*tenant, userID, roleID)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return fmt.Errorf("用户 %q 并未持有角色 %q", *email, *role)
			}
			if err := iamguard.RequireEffectiveAdministrator(ctx, tx, *tenant); err != nil {
				return err
			}
			return audit.Write(ctx, tx, *tenant, audit.Entry{
				ActorKind: "system", ActorLabel: "aegis-adminctl",
				Action: "adminctl.role_revoked", ResourceType: "user", ResourceID: &userID,
				APIDomain: "admin", Outcome: "success",
				BeforeDigest: map[string]any{"role": *role},
			})
		}

		var expires any
		if *hours > 0 {
			if err := tx.QueryRow(ctx,
				`SELECT now() + make_interval(hours => $1::int)`, *hours).Scan(&expires); err != nil {
				return err
			}
		}
		if err := upsertTenantRoleBinding(ctx, tx, *tenant, userID, roleID,
			"由 aegis-adminctl 授予", expires); err != nil {
			return err
		}
		if err := iamguard.RequireEffectiveAdministrator(ctx, tx, *tenant); err != nil {
			return err
		}
		return audit.Write(ctx, tx, *tenant, audit.Entry{
			ActorKind: "system", ActorLabel: "aegis-adminctl",
			Action: "adminctl.role_granted", ResourceType: "user", ResourceID: &userID,
			APIDomain: "admin", Outcome: "success",
			AfterDigest: map[string]any{"role": *role, "expires_at": expires},
		})
	})
	if err != nil {
		return err
	}

	if grant {
		if *hours > 0 {
			fmt.Printf("已授予 %s 角色 %s，%d 小时后自动回收\n", *email, *role, *hours)
		} else {
			fmt.Printf("已授予 %s 角色 %s\n", *email, *role)
		}
	} else {
		fmt.Printf("已回收 %s 的角色 %s\n", *email, *role)
	}
	return nil
}

// PostgreSQL treats NULL values as distinct in the legacy four-column UNIQUE
// constraint. Updating first under the tenant invariant lock prevents new
// duplicate tenant-scope bindings until the forward database migration can add
// a NULLS NOT DISTINCT constraint.
func upsertTenantRoleBinding(
	ctx context.Context, tx pgx.Tx, tenantID, userID, roleID, reason string, expires any,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE role_bindings
		   SET expires_at = $4, grant_reason = $5
		 WHERE tenant_id = $1 AND user_id = $2 AND role_id = $3
		   AND scope_type = 'tenant' AND scope_id IS NULL`,
		tenantID, userID, roleID, expires, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO role_bindings
			(tenant_id, user_id, role_id, scope_type, scope_id, grant_reason, expires_at)
		VALUES ($1, $2, $3, 'tenant', NULL, $4, $5)`,
		tenantID, userID, roleID, reason, expires)
	return err
}

func list(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	tenant := fs.String("tenant", defaultTenant, "租户 ID")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	return pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT u.email, u.status,
			       -- DISTINCT 是必须的：下面 JOIN role_permissions 会把每个角色
			       -- 放大成「权限数」那么多行，不去重的话角色名会重复几十次
			       string_agg(DISTINCT r.code, ',') AS roles,
			       count(DISTINCT rp.permission_code) AS perms,
			       to_char(u.last_login_at, 'YYYY-MM-DD HH24:MI')
			  FROM users u
			  JOIN role_bindings rb ON rb.user_id = u.id
			   AND (rb.expires_at IS NULL OR rb.expires_at > now())
			  JOIN roles r ON r.id = rb.role_id
			  LEFT JOIN role_permissions rp ON rp.role_id = r.id
			 WHERE u.tenant_id = $1
			 GROUP BY u.id, u.email, u.status, u.last_login_at
			 ORDER BY u.email`, *tenant)
		if err != nil {
			return err
		}
		defer rows.Close()

		fmt.Printf("%-32s %-10s %-28s %-7s %s\n", "邮箱", "状态", "角色", "权限数", "最近登录")
		n := 0
		for rows.Next() {
			var email, status, rls string
			var perms int
			var last *string
			if err := rows.Scan(&email, &status, &rls, &perms, &last); err != nil {
				return err
			}
			l := "从未"
			if last != nil {
				l = *last
			}
			fmt.Printf("%-32s %-10s %-28s %-7d %s\n", email, status, rls, perms, l)
			n++
		}
		if n == 0 {
			fmt.Println("(没有任何持有角色的账号，先执行 aegis-adminctl create)")
		}
		return rows.Err()
	})
}

func roles(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("roles", flag.ExitOnError)
	tenant := fs.String("tenant", defaultTenant, "租户 ID")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	return pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT r.code, r.name, count(rp.permission_code)
			  FROM roles r
			  LEFT JOIN role_permissions rp ON rp.role_id = r.id
			 WHERE r.tenant_id = $1
			 GROUP BY r.id, r.code, r.name
			 ORDER BY count(rp.permission_code) DESC, r.code`, *tenant)
		if err != nil {
			return err
		}
		defer rows.Close()

		fmt.Printf("%-20s %-20s %s\n", "角色码", "名称", "权限数")
		for rows.Next() {
			var code, name string
			var n int
			if err := rows.Scan(&code, &name, &n); err != nil {
				return err
			}
			fmt.Printf("%-20s %-20s %d\n", code, name, n)
		}
		return rows.Err()
	})
}
