package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
)

// 设备数限制这一组的键。
const (
	keyDevicesMode   = "devices.mode"           // string：loose / strict
	keyDevicesGrace  = "devices.grace"          // int，0–5
	keyDevicesWindow = "devices.window_minutes" // int，只能是 5、10、30、60
)

// DeviceModeLoose 表示每个节点各自限设备数。
const DeviceModeLoose = "loose"

// DeviceModeStrict 表示跨节点去重后超限的订阅整条摘掉。
const DeviceModeStrict = "strict"

// DeviceMaxGrace 是宽容值上限。
const DeviceMaxGrace = 5

const (
	defaultDeviceMode          = DeviceModeLoose
	defaultDeviceGrace         = 1
	defaultDeviceWindowMinutes = 5
	auditDevicesChanged        = "devices.settings_changed"
)

var devicesKeys = []string{keyDevicesGrace, keyDevicesMode, keyDevicesWindow}

// DevicePolicy 是设备数限制的判定策略。
type DevicePolicy struct {
	Mode          string // loose：每个节点各自限；strict：跨节点去重后超限的订阅整条摘掉
	Grace         int    // 宽容值：在线设备数超过 上限+Grace 才算超限
	WindowMinutes int    // 设备识别窗口（分钟）：最近这么久内上报过的来源算在线
}

// Strict 报告是否严格模式。
func (p DevicePolicy) Strict() bool { return p.Mode == DeviceModeStrict }

// Window 是识别窗口的时长。
func (p DevicePolicy) Window() time.Duration { return time.Duration(p.WindowMinutes) * time.Minute }

// DevicePolicyUpdate 是 SetDevicePolicy 的输入：Mode 必填；Grace、WindowMinutes 为 nil 表示不改。
type DevicePolicyUpdate struct {
	Mode          string
	Grace         *int
	WindowMinutes *int
}

// DefaultDevicePolicy 返回设备限制的缺省策略。
func DefaultDevicePolicy() DevicePolicy {
	return DevicePolicy{
		Mode:          defaultDeviceMode,
		Grace:         defaultDeviceGrace,
		WindowMinutes: defaultDeviceWindowMinutes,
	}
}

// ValidDeviceWindow 判断设备识别窗口（分钟）是否为允许值。
func ValidDeviceWindow(m int) bool {
	switch m {
	case 5, 10, 30, 60:
		return true
	default:
		return false
	}
}

// ValidateDevicePolicyUpdate 校验 SetDevicePolicy 的输入。
func ValidateDevicePolicyUpdate(u DevicePolicyUpdate) error {
	fields := map[string]string{}
	if u.Mode != DeviceModeLoose && u.Mode != DeviceModeStrict {
		fields["mode"] = "模式只能是 loose 或 strict"
	}
	if u.Grace != nil && (*u.Grace < 0 || *u.Grace > DeviceMaxGrace) {
		fields["grace"] = "宽容值需在 0 到 5 之间"
	}
	if u.WindowMinutes != nil && !ValidDeviceWindow(*u.WindowMinutes) {
		fields["window_minutes"] = "设备识别窗口只能是 5、10、30 或 60 分钟"
	}
	if len(fields) > 0 {
		return errs.Invalid(fields)
	}
	return nil
}

// decodeDeviceItem 解单项设置：缺行取 def；解码失败或 ok 不通过时记 warn 并取 def。
func decodeDeviceItem[T any](ctx context.Context, vals map[string]json.RawMessage, key string, def T, ok func(T) bool) T {
	raw, exists := vals[key]
	if !exists || len(raw) == 0 {
		return def
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		plog.From(ctx).Warn("设备限制设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	if !ok(v) {
		plog.From(ctx).Warn("设备限制设置的值非法，按缺省值", zap.String("key", key))
		return def
	}
	return v
}

// decodeDevicePolicy 从键值解出设备策略；非法项按缺省值。
func decodeDevicePolicy(ctx context.Context, vals map[string]json.RawMessage) DevicePolicy {
	mode := decodeDeviceItem(ctx, vals, keyDevicesMode, defaultDeviceMode, func(s string) bool {
		return s == DeviceModeLoose || s == DeviceModeStrict
	})
	grace := decodeDeviceItem(ctx, vals, keyDevicesGrace, defaultDeviceGrace, func(n int) bool {
		return n >= 0 && n <= DeviceMaxGrace
	})
	window := decodeDeviceItem(ctx, vals, keyDevicesWindow, defaultDeviceWindowMinutes, ValidDeviceWindow)
	return DevicePolicy{Mode: mode, Grace: grace, WindowMinutes: window}
}

// DevicePolicy 读取设备数限制策略。
func (s *Service) DevicePolicy(ctx context.Context) (DevicePolicy, error) {
	vals, err := s.store.Values(ctx, devicesKeys)
	if err != nil {
		return DevicePolicy{}, err
	}
	return decodeDevicePolicy(ctx, vals), nil
}

// SetDevicePolicy 在锁住本组各行的事务里合并、写入设备限制；有改动才写 devices.settings_changed 审计。
func (s *Service) SetDevicePolicy(ctx context.Context, u DevicePolicyUpdate) error {
	if err := ValidateDevicePolicyUpdate(u); err != nil {
		return err
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, devicesKeys)
		if err != nil {
			return err
		}
		old := decodeDevicePolicy(ctx, vals)
		v := old
		v.Mode = u.Mode
		if u.Grace != nil {
			v.Grace = *u.Grace
		}
		if u.WindowMinutes != nil {
			v.WindowMinutes = *u.WindowMinutes
		}
		changes, b, a := diff(devicesMap(old), devicesMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditDevicesChanged, auditResourceSettings, map[string]any{
			"group":  "devices",
			"before": b,
			"after":  a,
		})
	})
}

// devicesMap 把设备策略摊成「键 → 存储值」。
func devicesMap(p DevicePolicy) map[string]any {
	return map[string]any{
		keyDevicesMode:   p.Mode,
		keyDevicesGrace:  p.Grace,
		keyDevicesWindow: p.WindowMinutes,
	}
}
