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
	// keySubscriptionPathPrefix 是订阅链接的路径前缀：12 位小写十六进制随机值，
	// 首次读取时生成并落库，之后只读、不提供修改入口（改了等于让所有已发出的链接失效）。
	keySubscriptionPathPrefix = "subscription.path_prefix" // string
	// keySubscriptionFetchLimit 是每条凭据每小时的拉取上限（缺省 60）。
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

// ensurePathPrefix 补上前缀：生成一个随机值，只在键不存在时写入（ON CONFLICT DO NOTHING），再读回库里的值。
// 并发首次生成（进程内由 prefixFlight 合并；跨进程如滚动重启时两个 serve 同时首读）时只有先写入的那个生效，
// 后到的读回的就是它，不会出现「先返回的值被覆盖」。不写审计：这是系统自动生成的值，不是谁改了设置。
func (s *Service) ensurePathPrefix(ctx context.Context) (string, error) {
	b := make([]byte, subscriptionPathPrefixBytes)
	if _, err := rand.Read(b); err != nil {
		return "", errs.Wrap(errs.Internal, fmt.Errorf("settings: 生成订阅路径前缀: %w", err))
	}
	raw, err := json.Marshal(hex.EncodeToString(b))
	if err != nil {
		return "", errs.Wrap(errs.Internal, err)
	}
	if _, err := s.store.InsertIfAbsent(ctx, keySubscriptionPathPrefix, raw); err != nil {
		return "", err
	}
	vals, err := s.store.Values(ctx, []string{keySubscriptionPathPrefix})
	if err != nil {
		return "", err
	}
	prefix, err := decodeOr(vals, keySubscriptionPathPrefix, "")
	if err != nil {
		return "", err
	}
	if prefix == "" {
		return "", errs.Wrap(errs.Internal, fmt.Errorf("settings: 写入 %s 后读不到", keySubscriptionPathPrefix))
	}
	return prefix, nil
}
