package service

import (
	"context"
	"encoding/json"
)

// 订阅附加设置这一组的键（信息节点、协议前缀）。
const (
	keySubscriptionInfoNodes      = "subscription.info_nodes"      // bool，缺省 false
	keySubscriptionProtocolPrefix = "subscription.protocol_prefix" // bool，缺省 false
)

const auditSubscriptionExtrasChanged = "subscription.settings_changed"

var subscriptionExtrasKeys = []string{keySubscriptionInfoNodes, keySubscriptionProtocolPrefix}

// SubscriptionExtras 是订阅附加设置。
type SubscriptionExtras struct {
	InfoNodes      bool // 订阅开头插「剩余流量 / 距下次重置 / 到期」信息节点
	ProtocolPrefix bool // 节点名前加协议前缀
}

// SubscriptionExtrasUpdate 是部分更新：nil 字段保持原值。
type SubscriptionExtrasUpdate struct {
	InfoNodes      *bool
	ProtocolPrefix *bool
}

func decodeSubscriptionExtras(vals map[string]json.RawMessage) (SubscriptionExtras, error) {
	info, err := decodeOr(vals, keySubscriptionInfoNodes, false)
	if err != nil {
		return SubscriptionExtras{}, err
	}
	prefix, err := decodeOr(vals, keySubscriptionProtocolPrefix, false)
	if err != nil {
		return SubscriptionExtras{}, err
	}
	return SubscriptionExtras{
		InfoNodes:      info,
		ProtocolPrefix: prefix,
	}, nil
}

func subscriptionExtrasMap(e SubscriptionExtras) map[string]any {
	return map[string]any{
		keySubscriptionInfoNodes:      e.InfoNodes,
		keySubscriptionProtocolPrefix: e.ProtocolPrefix,
	}
}

// SubscriptionExtras 读取订阅附加设置。
func (s *Service) SubscriptionExtras(ctx context.Context) (SubscriptionExtras, error) {
	vals, err := s.store.Values(ctx, subscriptionExtrasKeys)
	if err != nil {
		return SubscriptionExtras{}, err
	}
	return decodeSubscriptionExtras(vals)
}

// SetSubscriptionExtras 在锁住本组各行的事务里合并、写入订阅附加设置；有改动才写审计。
func (s *Service) SetSubscriptionExtras(ctx context.Context, u SubscriptionExtrasUpdate) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, subscriptionExtrasKeys)
		if err != nil {
			return err
		}
		old, err := decodeSubscriptionExtras(vals)
		if err != nil {
			return err
		}
		v := old
		if u.InfoNodes != nil {
			v.InfoNodes = *u.InfoNodes
		}
		if u.ProtocolPrefix != nil {
			v.ProtocolPrefix = *u.ProtocolPrefix
		}

		changes, b, a := diff(subscriptionExtrasMap(old), subscriptionExtrasMap(v))
		if len(changes) == 0 {
			return nil
		}
		if err := s.writeChanges(ctx, changes); err != nil {
			return err
		}
		return s.writeAudit(ctx, auditSubscriptionExtrasChanged, auditResourceSettings, map[string]any{
			"group":  "subscription",
			"before": b,
			"after":  a,
		})
	})
}
