package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 订阅附加设置这一组的键（信息节点、协议前缀、多个订阅域名）。
const (
	keySubscriptionInfoNodes      = "subscription.info_nodes"      // bool，缺省 false
	keySubscriptionProtocolPrefix = "subscription.protocol_prefix" // bool，缺省 false
	keySubscriptionLinkBaseURLs   = "subscription.link_base_urls"  // []string，缺省空
)

const (
	// SubscriptionLinkBaseURLsMax 是订阅域名最多几个。
	SubscriptionLinkBaseURLsMax = 10
	// subscriptionLinkBaseURLMaxLen 是单个地址的最大长度。
	subscriptionLinkBaseURLMaxLen  = 255
	auditSubscriptionExtrasChanged = "subscription.settings_changed"
)

var subscriptionExtrasKeys = []string{keySubscriptionInfoNodes, keySubscriptionLinkBaseURLs, keySubscriptionProtocolPrefix}

// SubscriptionExtras 是订阅附加设置。
type SubscriptionExtras struct {
	InfoNodes      bool     // 订阅开头插「剩余流量 / 距下次重置 / 到期」信息节点
	ProtocolPrefix bool     // 节点名前加协议前缀
	LinkBaseURLs   []string // 订阅链接地址前缀（已规范化：https、无末尾斜杠）；空用面板对外地址
}

// SubscriptionExtrasUpdate 是部分更新：nil 字段保持原值；LinkBaseURLs 非 nil 时整组替换（空切片 = 清空）。
type SubscriptionExtrasUpdate struct {
	InfoNodes      *bool
	ProtocolPrefix *bool
	LinkBaseURLs   *[]string
}

// NormalizeLinkBaseURLs 规范化并校验订阅链接地址前缀列表。
func NormalizeLinkBaseURLs(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for i, raw := range in {
		n := i + 1
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		v = strings.TrimRight(v, "/")
		if len(v) > subscriptionLinkBaseURLMaxLen {
			return nil, fmt.Errorf("第 %d 个订阅地址长度不能超过 %d", n, subscriptionLinkBaseURLMaxLen)
		}
		u, err := url.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("第 %d 个订阅地址格式不正确", n)
		}
		if u.Scheme != "https" {
			return nil, fmt.Errorf("第 %d 个订阅地址必须以 https:// 开头", n)
		}
		if u.Host == "" {
			return nil, fmt.Errorf("第 %d 个订阅地址必须包含域名", n)
		}
		if u.User != nil {
			return nil, fmt.Errorf("第 %d 个订阅地址不能包含用户名或密码", n)
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(v, "?") || strings.Contains(v, "#") {
			return nil, fmt.Errorf("第 %d 个订阅地址只能写到域名（可带端口），不能带路径、参数", n)
		}
		if u.ForceQuery {
			return nil, fmt.Errorf("第 %d 个订阅地址只能写到域名（可带端口），不能带路径、参数", n)
		}
		host := strings.ToLower(u.Host)
		canonical := "https://" + host
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, canonical)
		if len(out) > SubscriptionLinkBaseURLsMax {
			return nil, fmt.Errorf("订阅地址最多 %d 个", SubscriptionLinkBaseURLsMax)
		}
	}
	return out, nil
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
	urls, err := decodeOr(vals, keySubscriptionLinkBaseURLs, []string{})
	if err != nil {
		return SubscriptionExtras{}, err
	}
	normalized, err := NormalizeLinkBaseURLs(urls)
	if err != nil {
		return SubscriptionExtras{}, errs.Wrap(errs.Internal, err)
	}
	return SubscriptionExtras{
		InfoNodes:      info,
		ProtocolPrefix: prefix,
		LinkBaseURLs:   normalized,
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
	if u.LinkBaseURLs != nil {
		if _, err := NormalizeLinkBaseURLs(*u.LinkBaseURLs); err != nil {
			return errs.Invalid(map[string]string{"link_base_urls": err.Error()})
		}
	}

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
		if u.LinkBaseURLs != nil {
			normalized, err := NormalizeLinkBaseURLs(*u.LinkBaseURLs)
			if err != nil {
				return errs.Invalid(map[string]string{"link_base_urls": err.Error()})
			}
			v.LinkBaseURLs = normalized
		}

		changes, b, a := diff(subscriptionExtrasMap(old), subscriptionExtrasMap(v))
		if !slices.Equal(old.LinkBaseURLs, v.LinkBaseURLs) {
			changes = append(changes, change{key: keySubscriptionLinkBaseURLs, value: v.LinkBaseURLs})
			b[keySubscriptionLinkBaseURLs] = old.LinkBaseURLs
			a[keySubscriptionLinkBaseURLs] = v.LinkBaseURLs
		}
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
