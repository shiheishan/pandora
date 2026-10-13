package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"

	"golang.org/x/sync/singleflight"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// 订阅分发这一组的键。
const (
	// keySubscriptionPathPrefix 是订阅链接的路径前缀（旧库 tenants.sub_path_prefix）：12 位小写十六进制随机值，
	// 首次读取时生成并落库，之后只读、不提供修改入口（改了等于让所有已发出的链接失效）。
	keySubscriptionPathPrefix = "subscription.path_prefix" // string
	// keySubscriptionFetchLimit 是每条凭据每小时的拉取上限（旧库凭据上的 rate_limit_per_hour，缺省 60）。
	keySubscriptionFetchLimit = "subscription.fetch_limit_per_hour" // int，1–10000
)

const (
	defaultSubscriptionFetchLimit = 60
	// SubscriptionFetchLimitMax 是每小时拉取上限的上限：再高限流就形同虚设。
	SubscriptionFetchLimitMax = 10000
	// subscriptionPathPrefixBytes 是前缀的随机字节数（12 位十六进制）。
	subscriptionPathPrefixBytes = 6
)

// subscriptionPathPrefixRe 是合法前缀：12 位小写十六进制（永远不会与 assets、healthz、v1 这类字面路由同名）。
var subscriptionPathPrefixRe = regexp.MustCompile(`^[0-9a-f]{12}$`)

// prefixFlight 把同一进程里并发的首次生成合成一次（单实例部署，进程内合并就够了；库里另有锁兜底，见 ensurePathPrefix）。
var prefixFlight singleflight.Group

// SubscriptionDelivery 是订阅分发的设置。
type SubscriptionDelivery struct {
	// PathPrefix 是订阅链接的路径前缀（12 位十六进制）
	PathPrefix string
	// FetchLimitPerHour 是每条凭据每小时的拉取上限
	FetchLimitPerHour int
}

// SubscriptionDelivery 读订阅分发的设置；前缀还没有时生成并落库（只发生一次）。
func (s *Service) SubscriptionDelivery(ctx context.Context) (SubscriptionDelivery, error) {
	vals, err := s.store.Values(ctx, []string{keySubscriptionPathPrefix, keySubscriptionFetchLimit})
	if err != nil {
		return SubscriptionDelivery{}, err
	}
	prefix, err := decodeOr(vals, keySubscriptionPathPrefix, "")
	if err != nil {
		return SubscriptionDelivery{}, err
	}
	limit, err := decodeOr(vals, keySubscriptionFetchLimit, defaultSubscriptionFetchLimit)
	if err != nil {
		return SubscriptionDelivery{}, err
	}
	if limit < 1 || limit > SubscriptionFetchLimitMax {
		return SubscriptionDelivery{}, errs.Wrap(errs.Internal, fmt.Errorf("settings: %s=%d 超出 1–%d", keySubscriptionFetchLimit, limit, SubscriptionFetchLimitMax))
	}
	if prefix == "" {
		v, err, _ := prefixFlight.Do(keySubscriptionPathPrefix, func() (any, error) {
			return s.ensurePathPrefix(ctx)
		})
		if err != nil {
			return SubscriptionDelivery{}, err
		}
		prefix = v.(string)
	}
	if !subscriptionPathPrefixRe.MatchString(prefix) {
		// 库里的值被手工改坏：照内部错误处理，不自动重生成（会让已发出的链接全部失效）
		return SubscriptionDelivery{}, errs.Wrap(errs.Internal, fmt.Errorf("settings: %s 不是 12 位十六进制", keySubscriptionPathPrefix))
	}
	return SubscriptionDelivery{PathPrefix: prefix, FetchLimitPerHour: limit}, nil
}

// ensurePathPrefix 在事务里补上前缀：先锁读，已有（别的请求刚写好）就用它，没有才生成写入。
// 不写审计：这是系统自动生成的值，不是谁改了设置。
//
// 跨进程的并发首次生成：两边都读到「没有」时，后提交的一方会覆盖前一方（Upsert），先返回的那一方手里的值随即作废。
// 部署是单实例（serve 一个进程），进程内已由 prefixFlight 合并；worker 不读前缀。
func (s *Service) ensurePathPrefix(ctx context.Context) (string, error) {
	var prefix string
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		vals, err := s.store.LockValues(ctx, []string{keySubscriptionPathPrefix})
		if err != nil {
			return err
		}
		existing, err := decodeOr(vals, keySubscriptionPathPrefix, "")
		if err != nil {
			return err
		}
		if existing != "" {
			prefix = existing
			return nil
		}
		b := make([]byte, subscriptionPathPrefixBytes)
		if _, err := rand.Read(b); err != nil {
			return errs.Wrap(errs.Internal, fmt.Errorf("settings: 生成订阅路径前缀: %w", err))
		}
		prefix = hex.EncodeToString(b)
		raw, err := json.Marshal(prefix)
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		// updated_by 留空：系统生成，不记到触发这次读取的人头上
		return s.store.Upsert(ctx, keySubscriptionPathPrefix, raw, nil)
	})
	if err != nil {
		return "", err
	}
	return prefix, nil
}
