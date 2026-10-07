package bindingcontract

import (
	"errors"
	"strconv"
	"strings"
)

// UsersKindFull 是整份名单。增量名单以后另加 kind 取值，靠 features 协商后才会出现。
const UsersKindFull = "full"

// UsersFields 是按池名单响应（S4）签名覆盖的全部字段。名单本体（payload 的原始字节）
// 经 content_sha256 间接覆盖；request_nonce 把这份签名绑到发起请求的那一次，
// 截获的响应不能拿去回答另一个请求，落盘缓存连同 nonce 一起存，启动时照样能重新验签。
type UsersFields struct {
	TenantID      string
	ServerID      string
	Serial        int64
	PoolID        string
	UsersVersion  uint64 // 与 ETag（池，版本）中的版本相同
	Kind          string // 目前只有 full
	UserCount     int
	ContentSHA256 string // base64(sha256(payload 原始字节))
	RequestNonce  string
	KeyID         string
	IssuedAt      string
	ExpiresAt     string
}

// UsersPreimage 返回面板配置私钥签名、pdnd 独立重建的名单原像。
func UsersPreimage(in UsersFields) ([]byte, error) {
	if err := checkUUID("tenant_id", in.TenantID); err != nil {
		return nil, err
	}
	if err := checkUUID("server_id", in.ServerID); err != nil {
		return nil, err
	}
	if err := checkSerial(in.Serial); err != nil {
		return nil, err
	}
	if err := checkUUID("pool_id", in.PoolID); err != nil {
		return nil, err
	}
	if err := checkGeneration("users_version", in.UsersVersion); err != nil {
		return nil, err
	}
	if in.Kind != UsersKindFull {
		return nil, errors.New("kind must be full")
	}
	if in.UserCount < 0 || in.UserCount > 10_000_000 {
		return nil, errors.New("user_count must be within [0, 10000000]")
	}
	if err := checkSHA256B64("content_sha256", in.ContentSHA256); err != nil {
		return nil, err
	}
	if err := checkNonce("request_nonce", in.RequestNonce); err != nil {
		return nil, err
	}
	if err := checkKeyID("key_id", in.KeyID); err != nil {
		return nil, err
	}
	if err := canonicalWindow(in.IssuedAt, in.ExpiresAt); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(480)
	b.WriteString(UsersContract)
	b.WriteByte('\n')
	line(&b, "tenant_id", in.TenantID)
	line(&b, "server_id", in.ServerID)
	line(&b, "serial", formatInt(in.Serial))
	line(&b, "pool_id", in.PoolID)
	line(&b, "users_version", formatUint(in.UsersVersion))
	line(&b, "kind", in.Kind)
	line(&b, "user_count", strconv.Itoa(in.UserCount))
	line(&b, "content_sha256", in.ContentSHA256)
	line(&b, "request_nonce", in.RequestNonce)
	line(&b, "key_id", in.KeyID)
	line(&b, "issued_at", in.IssuedAt)
	line(&b, "expires_at", in.ExpiresAt)
	return []byte(b.String()), nil
}
