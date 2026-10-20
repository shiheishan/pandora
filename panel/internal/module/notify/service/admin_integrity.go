package service

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// IntegrityAlert 是 report 的数据巡检要发的一条管理员群组告警：只带检查名与违例数，不带样例与业务数据。
type IntegrityAlert struct {
	Code  string    // 检查代号（如 ledger_drift），与运维手册「数据巡检告警」的小节对应
	Name  string    // 检查的中文名
	Count int64     // 本轮违例数（> 0）
	Since time.Time // 这一轮违例从什么时候开始（从 0 变成非 0 的那次巡检），区分前后两段违例的去重键
}

// NotifyAdminsIntegrity 给管理员群组排一条数据巡检告警（在调用方事务里，s.tx.Do 加入）。
// 没有后台开关：adminChat 不可发（Telegram 没开或没配管理员群组）时什么都不写、返回 false, nil，与其他管理员提醒同口径。
// 去重键 "integrity:<code>:<Since 秒>:<count>"：同一段违例、同一个数只发一次，调用方重跑不会重发；回到 0 后再出现是新的一段。
// 返回 true 表示这条告警已排进投递（新写的，或撞去重键说明早已排过）；false 表示群组不可发、什么都没写，
// report 据此决定记不记「上次告警时的数」：没配群组时不记，配好后下一轮照样会发。
func (s *Service) NotifyAdminsIntegrity(ctx context.Context, in IntegrityAlert) (bool, error) {
	if in.Code == "" || in.Count <= 0 || in.Since.IsZero() {
		return false, errs.Wrap(errs.Internal, fmt.Errorf("notify: 巡检告警参数无效 %q %d", in.Code, in.Count))
	}
	chatID, ok, err := s.adminChat(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	count := strconv.FormatInt(in.Count, 10)
	notice := adminNotice{
		Code: TemplateAdminIntegrityAlert,
		Vars: map[string]string{
			"check": in.Name,
			"code":  in.Code,
			"count": count,
		},
		Key: "integrity:" + in.Code + ":" + strconv.FormatInt(in.Since.Unix(), 10) + ":" + count,
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		_, err := s.writeAdminNotice(ctx, chatID, notice)
		return err
	})
	if err != nil {
		return false, err
	}
	return true, nil
}
