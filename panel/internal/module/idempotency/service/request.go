package service

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"time"
)

const (
	// HeaderKey 是客户端提交幂等键的请求头名。
	HeaderKey = "Idempotency-Key"
	// MaxBodyBytes 是请求体与可重放响应体的上限。
	MaxBodyBytes = 1 << 20
	// LeaseDuration 是认领与接管的租约时长。
	LeaseDuration = 5 * time.Minute
	// Retention 是幂等记录建议保留时长（清理任务使用）。
	Retention = 30 * 24 * time.Hour
)

// Fingerprint 是请求的 SHA-256 指纹。
type Fingerprint [sha256.Size]byte

// ValidKey 判断 Idempotency-Key 是否合法。
func ValidKey(key string) bool {
	if len(key) == 0 || len(key) > 255 {
		return false
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] == 0x7f {
			return false
		}
	}
	return true
}

// ValidScope 判断 scope 是否匹配 [a-z0-9_]{1,64}。
func ValidScope(scope string) bool {
	if len(scope) == 0 || len(scope) > 64 {
		return false
	}
	for _, character := range scope {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

// Target 从 URL 得到指纹用的 path+query 目标串。
func Target(u *url.URL) string {
	target := u.EscapedPath()
	if target == "" {
		target = "/"
	}
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}
	return target
}

// NewFingerprint 计算 method、target、body 的请求指纹。
func NewFingerprint(method, target string, body []byte) Fingerprint {
	hashInput := make([]byte, 0, len(method)+len(target)+len(body)+2)
	hashInput = append(hashInput, method...)
	hashInput = append(hashInput, '\n')
	hashInput = append(hashInput, target...)
	hashInput = append(hashInput, '\n')
	hashInput = append(hashInput, body...)
	return sha256.Sum256(hashInput)
}

// validateResourceType 校验业务资源类型格式（与 scope 同规则）。
func validateResourceType(typ string) error {
	if !ValidScope(typ) {
		return fmt.Errorf("idempotency: invalid resource type %q", typ)
	}
	return nil
}
