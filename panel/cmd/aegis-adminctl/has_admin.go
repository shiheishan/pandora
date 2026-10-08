package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

// exitNoAdministrator 是 has-admin 在「没有可登录的有效管理员」时的退出码。
// 与连不上库、配置错误（退出码 1）分开，安装器据此决定要不要现场建第一个管理员。
const exitNoAdministrator = 3

// exitCodeError 让子命令带着指定的退出码结束，而不是一律 1。
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

// exitCode 取错误对应的进程退出码。
func exitCode(err error) int {
	var coded *exitCodeError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}

// hasAdmin 回答「这个租户里有没有可登录的有效管理员」，口径与后台改角色、停用账号时的
// 「至少保留一个有效管理员」守卫相同（iamguard.RequireEffectiveAdministrator）。
// 只读：不取锁、不写审计。有则退出 0，没有则退出 3。
func hasAdmin(ctx context.Context, pool *db.Pool) error {
	fs := flag.NewFlagSet("has-admin", flag.ContinueOnError)
	tenant := fs.String("tenant", defaultTenant, "租户 ID")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	var present bool
	err := pool.InTx(ctx, db.Scope{TenantID: *tenant}, func(tx pgx.Tx) error {
		var err error
		present, err = administratorPresent(ctx, tx, *tenant)
		return err
	})
	if err != nil {
		return err
	}
	if !present {
		return &exitCodeError{code: exitNoAdministrator, msg: "没有可登录的有效管理员，用 aegis-adminctl create 建一个"}
	}
	fmt.Println("已有可登录的有效管理员")
	return nil
}

// administratorPresent 把守卫的「没有有效管理员」错误翻成 false，其余错误原样返回。
func administratorPresent(ctx context.Context, tx pgx.Tx, tenant string) (bool, error) {
	err := iamguard.RequireEffectiveAdministrator(ctx, tx, tenant)
	if errors.Is(err, iamguard.ErrLastEffectiveAdministrator) {
		return false, nil
	}
	return err == nil, err
}
