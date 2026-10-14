// perfseed 是 P8 性能测试造数工具的命令行入口。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const usage = `用法:
  perfseed infra    --admin-url URL --admin-email E --admin-password-file F --out-dir D [--servers 200]
  perfseed activate --admin-url URL --admin-email E --admin-password-file F --out-dir D
  perfseed users    --dsn-file F --out-dir D --user-password-file F2 [--secrets-env /etc/pandora/secrets.env]
                    [--users 5000] [--expire-at RFC3339 --expire-count N] [--history-days 30]
                    [--expired-percent 10] [--seed 1]

子命令:
  infra     经后台 API 建服务器与节点，写 tokens、nodesim.env 等
  activate  节点上线并发布套餐，写 catalog.json
  users     直连库批量写用户与订阅，写 users.csv
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	if cmd == "-h" || cmd == "--help" || cmd == "help" {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dispatch(ctx, cmd, os.Args[2:]); err != nil {
		if err == errUnknownCommand {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "perfseed: %v\n", err)
		os.Exit(1)
	}
}

// errUnknownCommand 是子命令不认识（打印用法，退出码 2）。
var errUnknownCommand = errors.New("未知子命令")

// dispatch 解析子命令参数并执行。
func dispatch(ctx context.Context, cmd string, args []string) error {
	switch cmd {
	case "infra":
		o, err := parseInfraArgs(args)
		if err != nil {
			return err
		}
		return runInfra(ctx, o)
	case "activate":
		o, err := parseActivateArgs(args)
		if err != nil {
			return err
		}
		return runActivate(ctx, o)
	case "users":
		o, err := parseUsersArgs(args, time.Now().UTC())
		if err != nil {
			return err
		}
		return runUsers(ctx, o)
	}
	return errUnknownCommand
}

func parseAdminFlags(fs *flag.FlagSet, o *adminOpts) {
	fs.StringVar(&o.AdminURL, "admin-url", "", "后台基地址")
	fs.StringVar(&o.AdminEmail, "admin-email", "", "管理员邮箱")
	fs.StringVar(&o.AdminPasswordFile, "admin-password-file", "", "管理员口令文件")
	fs.StringVar(&o.OutDir, "out-dir", "", "输出目录")
}

func validateAdminOpts(o adminOpts) error {
	if o.AdminURL == "" {
		return fmt.Errorf("--admin-url 为必填项")
	}
	u, err := url.Parse(o.AdminURL)
	if err != nil {
		return fmt.Errorf("--admin-url 不是合法 URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("--admin-url 的 scheme 只能是 http 或 https")
	}
	if u.Host == "" {
		return fmt.Errorf("--admin-url 缺少主机名")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("--admin-url 不能带查询串或片段")
	}
	if o.AdminEmail == "" {
		return fmt.Errorf("--admin-email 为必填项")
	}
	if !strings.Contains(o.AdminEmail, "@") {
		return fmt.Errorf("--admin-email 须包含 @")
	}
	if o.AdminPasswordFile == "" {
		return fmt.Errorf("--admin-password-file 为必填项")
	}
	if o.OutDir == "" {
		return fmt.Errorf("--out-dir 为必填项")
	}
	return nil
}

func normalizeAdminURL(raw string) string {
	return strings.TrimSuffix(raw, "/")
}

// parseInfraArgs 解析 infra 子命令参数。
func parseInfraArgs(args []string) (infraOpts, error) {
	fs := flag.NewFlagSet("infra", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o infraOpts
	parseAdminFlags(fs, &o.adminOpts)
	fs.IntVar(&o.Servers, "servers", 200, "服务器台数")
	if err := fs.Parse(args); err != nil {
		return infraOpts{}, err
	}
	if fs.NArg() > 0 {
		return infraOpts{}, fmt.Errorf("未知参数: %s", fs.Arg(0))
	}
	if err := validateAdminOpts(o.adminOpts); err != nil {
		return infraOpts{}, err
	}
	o.AdminURL = normalizeAdminURL(o.AdminURL)
	if o.Servers < 1 || o.Servers > 1000 {
		return infraOpts{}, fmt.Errorf("--servers 须在 1–1000 之间，当前为 %d", o.Servers)
	}
	return o, nil
}

// parseActivateArgs 解析 activate 子命令参数。
func parseActivateArgs(args []string) (activateOpts, error) {
	fs := flag.NewFlagSet("activate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o activateOpts
	parseAdminFlags(fs, &o.adminOpts)
	if err := fs.Parse(args); err != nil {
		return activateOpts{}, err
	}
	if fs.NArg() > 0 {
		return activateOpts{}, fmt.Errorf("未知参数: %s", fs.Arg(0))
	}
	if err := validateAdminOpts(o.adminOpts); err != nil {
		return activateOpts{}, err
	}
	o.AdminURL = normalizeAdminURL(o.AdminURL)
	return o, nil
}

// parseUsersArgs 解析 users 子命令参数；now 用于校验 --expire-at。
func parseUsersArgs(args []string, now time.Time) (usersOpts, error) {
	fs := flag.NewFlagSet("users", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o usersOpts
	var expireAtStr string
	var seed uint64
	fs.StringVar(&o.DSNFile, "dsn-file", "", "属主角色 DSN 文件")
	fs.StringVar(&o.SecretsEnv, "secrets-env", "/etc/pandora/secrets.env", "面板 secrets.env")
	fs.StringVar(&o.OutDir, "out-dir", "", "输出目录")
	fs.IntVar(&o.Users, "users", 5000, "用户数")
	fs.StringVar(&o.UserPasswordFile, "user-password-file", "", "用户共用口令文件")
	fs.StringVar(&expireAtStr, "expire-at", "", "订阅到期时间 RFC3339")
	fs.IntVar(&o.ExpireCount, "expire-count", 0, "到期突发用户数")
	fs.IntVar(&o.HistoryDays, "history-days", 30, "按天用量历史天数")
	fs.IntVar(&o.ExpiredPercent, "expired-percent", 10, "已过期用户百分比")
	fs.Uint64Var(&seed, "seed", 1, "随机种子")
	if err := fs.Parse(args); err != nil {
		return usersOpts{}, err
	}
	if fs.NArg() > 0 {
		return usersOpts{}, fmt.Errorf("未知参数: %s", fs.Arg(0))
	}
	if o.DSNFile == "" {
		return usersOpts{}, fmt.Errorf("--dsn-file 为必填项")
	}
	if o.OutDir == "" {
		return usersOpts{}, fmt.Errorf("--out-dir 为必填项")
	}
	if o.UserPasswordFile == "" {
		return usersOpts{}, fmt.Errorf("--user-password-file 为必填项")
	}
	if o.Users < 1 || o.Users > 100000 {
		return usersOpts{}, fmt.Errorf("--users 须在 1–100000 之间，当前为 %d", o.Users)
	}
	if o.HistoryDays < 0 || o.HistoryDays > 400 {
		return usersOpts{}, fmt.Errorf("--history-days 须在 0–400 之间，当前为 %d", o.HistoryDays)
	}
	if o.ExpiredPercent < 0 || o.ExpiredPercent > 50 {
		return usersOpts{}, fmt.Errorf("--expired-percent 须在 0–50 之间，当前为 %d", o.ExpiredPercent)
	}
	o.Seed = seed

	if o.ExpireCount < 0 {
		return usersOpts{}, fmt.Errorf("--expire-count 不能为负数")
	}
	hasExpireAt := expireAtStr != ""
	hasExpireCount := o.ExpireCount != 0
	if hasExpireAt != hasExpireCount {
		return usersOpts{}, fmt.Errorf("--expire-at 与 --expire-count 须同时指定或同时省略")
	}
	if hasExpireAt {
		t, err := time.Parse(time.RFC3339, expireAtStr)
		if err != nil {
			return usersOpts{}, fmt.Errorf("--expire-at 不是合法的 RFC3339 时间: %w", err)
		}
		t = t.UTC()
		minAt := now.Add(10 * time.Minute)
		maxAt := now.Add(365 * 24 * time.Hour)
		if t.Before(minAt) {
			return usersOpts{}, fmt.Errorf("--expire-at 须晚于当前时间至少 10 分钟")
		}
		if t.After(maxAt) {
			return usersOpts{}, fmt.Errorf("--expire-at 不能晚于当前时间 365 天")
		}
		activeUsers := o.Users - o.Users*o.ExpiredPercent/100
		if o.ExpireCount > activeUsers {
			return usersOpts{}, fmt.Errorf("--expire-count 不能超过生效中的用户数 %d", activeUsers)
		}
		o.ExpireAt = &t
	}
	return o, nil
}
