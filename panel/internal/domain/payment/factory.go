package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Credentials 是各渠道凭据的通用载体。
// 存库前用信封加密（SEC-010），解密后才反序列化成这个结构。
type Credentials struct {
	// 易支付
	MerchantID string `json:"merchant_id,omitempty"`
	Key        string `json:"key,omitempty"`
	// 通用
	Extra map[string]string `json:"extra,omitempty"`
}

// ProviderRecord 是从 payment_providers 表读出的一行（凭据已解密）。
type ProviderRecord struct {
	ID          string
	Code        string
	Adapter     string
	DisplayName string
	Enabled     bool
	// AcceptingNew 为 false 时停止创建新支付，但已有支付仍可查询与回调（PAY-009）
	AcceptingNew bool
	Currencies   []string
	Config       map[string]any
	Credentials  Credentials
}

// Builder 把一条渠道记录构造成可用的 Provider 实例。
type Builder func(rec ProviderRecord) (Provider, error)

// Loader 从存储层读取渠道记录。由 billing 层注入，避免 payment 包依赖数据库。
type Loader func(ctx context.Context, tenantID, code string) (*ProviderRecord, error)

// Factory 按需构造并缓存 Provider 实例。
//
// 缓存的意义不只是省 CPU：每次回调都去解密一次凭据，
// 会让主密钥在内存中反复出现，也让渠道故障时的重试放大数据库压力。
// 缓存带 TTL，管理员改配置后最迟 TTL 到期生效。
type Factory struct {
	loader   Loader
	builders map[string]Builder
	ttl      time.Duration

	mu    sync.RWMutex
	cache map[string]*cacheEntry
}

type cacheEntry struct {
	provider  Provider
	record    ProviderRecord
	expiresAt time.Time
}

func NewFactory(loader Loader, ttl time.Duration) *Factory {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Factory{
		loader:   loader,
		builders: map[string]Builder{},
		ttl:      ttl,
		cache:    map[string]*cacheEntry{},
	}
}

// RegisterAdapter 登记一种适配器实现。adapter 对应 payment_providers.adapter 列。
func (f *Factory) RegisterAdapter(adapter string, b Builder) {
	f.builders[adapter] = b
}

// Get 返回指定租户下某渠道的 Provider 实例及其记录。
func (f *Factory) Get(ctx context.Context, tenantID, code string) (Provider, *ProviderRecord, error) {
	key := tenantID + "/" + code

	f.mu.RLock()
	if e, ok := f.cache[key]; ok && time.Now().Before(e.expiresAt) {
		f.mu.RUnlock()
		rec := e.record
		return e.provider, &rec, nil
	}
	f.mu.RUnlock()

	rec, err := f.loader(ctx, tenantID, code)
	if err != nil {
		return nil, nil, err
	}
	if !rec.Enabled {
		return nil, nil, fmt.Errorf("支付渠道 %q 未启用", code)
	}

	build, ok := f.builders[rec.Adapter]
	if !ok {
		return nil, nil, fmt.Errorf("未知的支付适配器 %q", rec.Adapter)
	}

	p, err := build(*rec)
	if err != nil {
		return nil, nil, fmt.Errorf("构造支付渠道 %q 失败: %w", code, err)
	}

	f.mu.Lock()
	f.cache[key] = &cacheEntry{provider: p, record: *rec, expiresAt: time.Now().Add(f.ttl)}
	f.mu.Unlock()

	return p, rec, nil
}

// Invalidate 在管理员修改渠道配置后立即清除缓存。
func (f *Factory) Invalidate(tenantID, code string) {
	f.mu.Lock()
	delete(f.cache, tenantID+"/"+code)
	f.mu.Unlock()
}

// DecodeCredentials 把解密后的凭据 JSON 反序列化。
func DecodeCredentials(plain []byte) (Credentials, error) {
	var c Credentials
	if len(plain) == 0 {
		return c, errors.New("凭据为空")
	}
	if err := json.Unmarshal(plain, &c); err != nil {
		return c, fmt.Errorf("凭据不是合法 JSON: %w", err)
	}
	return c, nil
}

// EncodeCredentials 序列化凭据，供管理端写入前加密。
func EncodeCredentials(c Credentials) ([]byte, error) {
	return json.Marshal(c)
}

// ConfigString 从渠道 config 里安全地取字符串值。
func ConfigString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}

// ConfigBool 从渠道 config 里安全地取布尔值。
func ConfigBool(cfg map[string]any, key string) bool {
	if cfg == nil {
		return false
	}
	if v, ok := cfg[key].(bool); ok {
		return v
	}
	return false
}
