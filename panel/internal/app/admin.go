package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/spf13/cobra"
)

// exitNoAdministrator 是 has-admin 在「没有可登录的有效管理员」时的退出码，与连不上库、配置错误（1）分开，安装器据此决定要不要现场建第一个管理员。
const exitNoAdministrator = 3

// exitCodeError 让子命令带着指定的退出码结束。
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

// newAdminCmd 是 pandora admin：在服务器本地管理后台管理员。后台不分角色：是管理员就能做全部后台操作。
// 第一个管理员只能从这里来（后台本身要登录才能进），所以不开放成 HTTP 接口。口令只从标准输入读，不进进程参数与 shell 历史。
func newAdminCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "管理后台管理员（create、reset-password、grant、revoke、list、has-admin）",
	}

	var email string
	var passwordStdin bool
	create := &cobra.Command{
		Use:   "create",
		Short: "建账号并设为管理员（口令从标准输入读）",
		RunE: func(c *cobra.Command, _ []string) error {
			if email == "" || !passwordStdin {
				return errors.New("--email 与 --password-stdin 必填")
			}
			password, err := readPasswordLine(c.InOrStdin())
			if err != nil {
				return err
			}
			return withServices(c.Context(), *configPath, func(ctx context.Context, svc *services) error {
				// 建账号与设为管理员同一个事务：授予失败不留下不是管理员的账号
				var id string
				err := svc.tx.Do(ctx, func(ctx context.Context) error {
					u, err := svc.identity.CreateUser(ctx, email, password)
					if err != nil {
						return err
					}
					id = u.ID.String()
					return svc.auth.GrantAdmin(ctx, u.ID)
				})
				if err != nil {
					return err
				}
				fmt.Fprintf(c.OutOrStdout(), "管理员已就绪\n  user_id = %s\n  email   = %s\n", id, email)
				return nil
			})
		},
	}
	create.Flags().StringVar(&email, "email", "", "管理员邮箱（必填）")
	create.Flags().BoolVar(&passwordStdin, "password-stdin", false, "从标准输入读口令（必填）")

	var resetEmail string
	var resetStdin bool
	reset := &cobra.Command{
		Use:   "reset-password",
		Short: "重置管理员口令、吊销其全部会话并清掉登录失败暂停（口令从标准输入读）",
		RunE: func(c *cobra.Command, _ []string) error {
			if resetEmail == "" || !resetStdin {
				return errors.New("--email 与 --password-stdin 必填")
			}
			password, err := readPasswordLine(c.InOrStdin())
			if err != nil {
				return err
			}
			return withServices(c.Context(), *configPath, func(ctx context.Context, svc *services) error {
				u, err := svc.identity.FindByEmail(ctx, resetEmail)
				if err != nil {
					return err
				}
				if err := svc.identity.ResetStaffPassword(ctx, u.ID, password); err != nil {
					return err
				}
				// 运维在服务器上重置口令就是要恢复登录，不该再等失败暂停过去；清不掉只提示，口令已经改好
				if err := svc.auth.ClearLoginFailures(ctx, u.ID); err != nil {
					fmt.Fprintf(c.ErrOrStderr(), "警告：清除登录失败计数失败（%v），若账号处于暂停期需等暂停结束\n", err)
				}
				fmt.Fprintf(c.OutOrStdout(), "管理员口令已重置，全部既有会话已失效\n  user_id = %s\n  email   = %s\n", u.ID, u.Email)
				return nil
			})
		},
	}
	reset.Flags().StringVar(&resetEmail, "email", "", "管理员邮箱（必填）")
	reset.Flags().BoolVar(&resetStdin, "password-stdin", false, "从标准输入读新口令（必填）")

	cmd.AddCommand(create, reset,
		newAdminBindCmd(configPath, true),
		newAdminBindCmd(configPath, false),
		newAdminListCmd(configPath),
		newHasAdminCmd(configPath),
	)
	return cmd
}

// newAdminBindCmd 是 grant / revoke：把已有账号设为管理员或取消。幂等：已是 / 已不是管理员时直接成功。
func newAdminBindCmd(configPath *string, grant bool) *cobra.Command {
	use, short, done := "grant", "把已有账号设为管理员（会吊销其门户会话）", "已设为管理员"
	if !grant {
		use, short, done = "revoke", "取消管理员（会吊销其后台会话；不能取消最后一个有效管理员）", "已取消管理员"
	}
	var email string
	c := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(c *cobra.Command, _ []string) error {
			if email == "" {
				return errors.New("--email 必填")
			}
			return withServices(c.Context(), *configPath, func(ctx context.Context, svc *services) error {
				u, err := svc.identity.FindByEmail(ctx, email)
				if err != nil {
					return err
				}
				if grant {
					err = svc.auth.GrantAdmin(ctx, u.ID)
				} else {
					err = svc.auth.RevokeAdmin(ctx, u.ID)
				}
				if err != nil {
					return err
				}
				fmt.Fprintf(c.OutOrStdout(), "%s：%s\n", done, u.Email)
				return nil
			})
		},
	}
	c.Flags().StringVar(&email, "email", "", "账号邮箱（必填）")
	return c
}

func newAdminListCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出管理员",
		RunE: func(c *cobra.Command, _ []string) error {
			return withServices(c.Context(), *configPath, func(ctx context.Context, svc *services) error {
				admins, err := svc.auth.ListAdmins(ctx)
				if err != nil {
					return err
				}
				w := tabwriter.NewWriter(c.OutOrStdout(), 0, 4, 2, ' ', 0)
				fmt.Fprintln(w, "USER_ID\tEMAIL\tSTATUS\tCREATED_AT")
				for _, a := range admins {
					fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", a.UserID, a.Email, a.Status, a.CreatedAt.UTC().Format(time.RFC3339))
				}
				return w.Flush()
			})
		},
	}
}

func newHasAdminCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "has-admin",
		Short: "有可登录的有效管理员时退出 0，没有时退出 3（安装器用）",
		RunE: func(c *cobra.Command, _ []string) error {
			return withServices(c.Context(), *configPath, func(ctx context.Context, svc *services) error {
				ok, err := svc.auth.HasAdmin(ctx)
				if err != nil {
					return err
				}
				if !ok {
					return &exitCodeError{code: exitNoAdministrator, msg: "没有可登录的有效管理员，用 pandora admin create 建一个"}
				}
				fmt.Fprintln(c.OutOrStdout(), "已有可登录的有效管理员")
				return nil
			})
		},
	}
}

// adminTimeout 是一条 pandora admin 命令的总时限：库或 Redis 不通时安装器不至于卡到 TCP 超时。
const adminTimeout = 60 * time.Second

// withServices 打开运行时与业务服务，执行 fn 后关闭。
func withServices(ctx context.Context, configPath string, fn func(context.Context, *services) error) error {
	ctx, cancel := context.WithTimeout(ctx, adminTimeout)
	defer cancel()
	r, err := openRuntime(ctx, configPath)
	if err != nil {
		return err
	}
	defer r.close()
	svc, err := buildServices(r)
	if err != nil {
		return err
	}
	defer svc.close()
	return cliError(fn(ctx, svc))
}

// cliError 把字段级校验错误（口令规则、邮箱格式）的明细带进命令行输出。
func cliError(err error) error {
	var e *errs.Error
	if !errors.As(err, &e) || len(e.Fields) == 0 {
		return err
	}
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return fmt.Errorf("%w（%s）", err, strings.Join(parts, "；"))
}

// readPasswordLine 从标准输入读一行口令（去掉行尾换行）。
func readPasswordLine(r io.Reader) (string, error) {
	value, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", fmt.Errorf("从标准输入读取口令: %w", err)
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if value == "" {
		return "", errors.New("标准输入中的口令不能为空")
	}
	return value, nil
}
