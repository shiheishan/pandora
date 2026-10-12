package service

import (
	"context"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"go.uber.org/zap"
)

// switchTTL 是开关在进程内缓存的时长：关掉后几秒内全部生效就够。
const switchTTL = 3 * time.Second

// switchLoadTimeout 是一次读开关的上限。
const switchLoadTimeout = 2 * time.Second

// SwitchChannel 是开关变更的 Redis pub/sub 频道；收到消息的进程清空本地缓存。
const SwitchChannel = "settings:switches"

type switchEntry struct {
	enabled bool
	exp     time.Time
}

// SwitchEnabled 读一个降级开关；缺行视为开启。
func (s *Service) SwitchEnabled(ctx context.Context, code string) (bool, error) {
	s.swMu.Lock()
	if e, ok := s.swCache[code]; ok && s.now().Before(e.exp) {
		enabled := e.enabled
		s.swMu.Unlock()
		return enabled, nil
	}
	gen := s.swGen
	s.swMu.Unlock()

	// 键带上纪元：失效之后来的调用不会挂到失效前开始的那趟读库上拿旧值。
	// 读库不跟随第一个调用者的取消，否则它被取消时同时在等的请求全部失败。
	v, err, _ := s.swGroup.Do(code+"#"+strconv.FormatUint(gen, 10), func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), switchLoadTimeout)
		defer cancel()
		sw, ok, err := s.store.GetSwitch(lctx, code)
		if err != nil {
			return false, err
		}
		enabled := !ok || sw.Enabled
		s.swMu.Lock()
		if s.swGen == gen {
			s.swCache[code] = switchEntry{enabled: enabled, exp: s.now().Add(switchTTL)}
		}
		s.swMu.Unlock()
		return enabled, nil
	})
	if err != nil {
		return false, err
	}
	return v.(bool), nil
}

// InvalidateSwitches 清空本进程的开关缓存并作废正在进行的加载。
func (s *Service) InvalidateSwitches() {
	s.swMu.Lock()
	s.swGen++
	clear(s.swCache)
	s.swMu.Unlock()
}

// Switches 返回全部降级开关。
func (s *Service) Switches(ctx context.Context) ([]Switch, error) {
	rows, err := s.store.ListSwitches(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Switch, 0, len(rows))
	for _, sw := range rows {
		out = append(out, Switch{
			Code:      sw.Code,
			Enabled:   sw.Enabled,
			Essential: sw.Essential,
			Reason:    sw.Reason,
		})
	}
	return out, nil
}

// SetSwitch 修改一个降级开关并写审计；提交后失效本进程缓存并广播。
func (s *Service) SetSwitch(ctx context.Context, code string, enabled bool, reason string) error {
	reason = strings.TrimSpace(reason)
	if utf8.RuneCountInString(reason) > 500 {
		return errs.Invalid(map[string]string{"reason": "超出上限"})
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		sw, ok, err := s.store.LockSwitch(ctx, code)
		if err != nil {
			return err
		}
		if !ok {
			return errs.New(errs.NotFound)
		}
		if sw.Essential && !enabled {
			return errs.Newf(errs.Conflict, "开关 "+code+" 不允许该操作：核心开关不能关闭")
		}
		if !enabled && reason == "" {
			return errs.Newf(errs.Conflict, "开关 "+code+" 不允许该操作：关闭开关必须填写原因")
		}
		var reasonPtr *string
		if reason != "" {
			reasonPtr = &reason
		}
		if err := s.store.UpdateSwitch(ctx, code, enabled, reasonPtr, actorID(ctx)); err != nil {
			return err
		}
		if err := s.writeAudit(ctx, auditSwitchToggle, auditResourceSwitch, map[string]any{
			"code":    code,
			"before":  sw.Enabled,
			"enabled": enabled,
			"reason":  reason,
		}); err != nil {
			return err
		}
		tx.AfterCommit(ctx, SwitchChannel, func(ctx context.Context) {
			s.InvalidateSwitches()
			s.publishSwitchChange(ctx)
		})
		return nil
	})
}

func (s *Service) publishSwitchChange(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	if err := s.rdb.Publish(ctx, SwitchChannel, "1").Err(); err != nil {
		plog.From(ctx).Warn("settings: 发布开关变更失败", zap.Error(err))
	}
}

// WatchSwitches 订阅开关变更频道，收到消息就清空本进程缓存；阻塞到 ctx 取消。serve 与 worker 各起一个 goroutine。
func (s *Service) WatchSwitches(ctx context.Context) {
	if s.rdb == nil {
		<-ctx.Done()
		return
	}
	ps := s.rdb.Subscribe(ctx, SwitchChannel)
	defer ps.Close()
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			s.InvalidateSwitches()
		}
	}
}
