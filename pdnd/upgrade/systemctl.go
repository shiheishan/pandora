package upgrade

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// ServiceManager 是助手重启主服务用的接口；测试换成假的。
type ServiceManager interface {
	Restart(ctx context.Context, unit string) error
}

// Systemctl 调真的 systemctl。Bin 空即 "systemctl"。
type Systemctl struct{ Bin string }

// Restart：ctx 加 90 秒上限；先 `systemctl reset-failed <unit>`（结果忽略：主单元有 StartLimitBurst，
// 崩溃循环撞了限制后不复位，restart 会被拒），再 `systemctl restart <unit>`，失败时错误里带退出原因与输出的前 200 字节。
func (s Systemctl) Restart(ctx context.Context, unit string) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	bin := s.Bin
	if bin == "" {
		bin = "systemctl"
	}

	_ = exec.CommandContext(ctx, bin, "reset-failed", unit).Run()

	cmd := exec.CommandContext(ctx, bin, "restart", unit)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		msg := out.String()
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("systemctl restart %s: %v: %s", unit, err, msg)
	}
	return nil
}
