package bindingcontract

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ServerRequestPathPrefix 是服务器身份签名接口的路径前缀。
const ServerRequestPathPrefix = "/v1/servers/"

const maxTargetLen = 512

// ServerRequestFields 是服务器请求签名覆盖的全部字段，逐个对应请求头：
// X-Server-Id、X-Server-Serial、X-Server-Ts、X-Server-Nonce、X-Report-Id，
// 方法与请求目标取自请求行，body_sha256 由接收方对收到的原始请求体重算。
type ServerRequestFields struct {
	Method     string
	Target     string // 路径，或「路径?查询」，必须是 CheckRequestTarget 认可的规范形式
	TenantID   string // 面板解析出的租户；pdnd 取自绑定文件
	ServerID   string
	Serial     int64
	Timestamp  string // RFC3339 UTC 秒，如 2026-10-07T08:00:00Z
	Nonce      string // 16 字节随机数的无填充 base64url
	ReportID   string // X-Report-Id；不带这个头时为空串
	BodySHA256 string // BodySHA256(请求体)
}

var allowedMethods = map[string]bool{"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true}

// ServerRequestPreimage 返回服务器身份用 Ed25519 签名、面板独立重建的原像字节。
func ServerRequestPreimage(in ServerRequestFields) ([]byte, error) {
	if !allowedMethods[in.Method] {
		return nil, errors.New("method must be one of GET, POST, PUT, PATCH, DELETE")
	}
	if err := CheckRequestTarget(in.Target); err != nil {
		return nil, err
	}
	if err := checkUUID("tenant_id", in.TenantID); err != nil {
		return nil, err
	}
	if err := checkUUID("server_id", in.ServerID); err != nil {
		return nil, err
	}
	if err := checkSerial(in.Serial); err != nil {
		return nil, err
	}
	if err := CheckRequestTimestamp(in.Timestamp); err != nil {
		return nil, err
	}
	if err := checkNonce("nonce", in.Nonce); err != nil {
		return nil, err
	}
	if in.ReportID != "" {
		if err := checkUUID("report_id", in.ReportID); err != nil {
			return nil, err
		}
	}
	if err := checkSHA256B64("body_sha256", in.BodySHA256); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(360)
	b.WriteString(ServerRequestContract)
	b.WriteByte('\n')
	line(&b, "method", in.Method)
	line(&b, "target", in.Target)
	line(&b, "tenant_id", in.TenantID)
	line(&b, "server_id", in.ServerID)
	line(&b, "serial", formatInt(in.Serial))
	line(&b, "ts", in.Timestamp)
	line(&b, "nonce", in.Nonce)
	line(&b, "report_id", in.ReportID)
	line(&b, "body_sha256", in.BodySHA256)
	return []byte(b.String()), nil
}

// CheckRequestTimestamp 要求 X-Server-Ts 是 RFC3339 UTC 秒的规范写法。
// 只校验格式；是否落在 ±RequestSkew 窗口内由验签方按自己的时钟判断。
func CheckRequestTimestamp(ts string) error {
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil || t.UTC().Format(time.RFC3339) != ts {
		return errors.New("ts must be canonical RFC3339 UTC seconds")
	}
	return nil
}

// CheckRequestTarget 校验请求目标的规范形式：
//   - 路径以 /v1/servers/ 开头，只含 [A-Za-z0-9._~/-]，没有空段、没有 . 与 .. 段、不以 / 结尾；
//   - 可选的查询串紧跟一个 ?，由 key=value 用 & 连接，键匹配 [a-z][a-z0-9_]{0,31}、严格升序且不重复，
//     值只含 [A-Za-z0-9._~-]、最长 128；? 后不能为空。
//
// 有了这些限制，路径与查询都不需要百分号编码，面板按 r.URL.EscapedPath() 与 r.URL.RawQuery
// 原样拼回即可得到同一串，不存在两种写法签同一个请求的歧义。
func CheckRequestTarget(target string) error {
	if len(target) > maxTargetLen {
		return fmt.Errorf("target exceeds %d bytes", maxTargetLen)
	}
	path, query, hasQuery := strings.Cut(target, "?")
	if !strings.HasPrefix(path, ServerRequestPathPrefix) || len(path) == len(ServerRequestPathPrefix) {
		return fmt.Errorf("target path must start with %s", ServerRequestPathPrefix)
	}
	for _, seg := range strings.Split(path[1:], "/") {
		if seg == "" || seg == "." || seg == ".." {
			return errors.New("target path must not contain empty, . or .. segments")
		}
		for i := 0; i < len(seg); i++ {
			if !isUnreserved(seg[i]) {
				return errors.New("target path contains a character outside [A-Za-z0-9._~-]")
			}
		}
	}
	if !hasQuery {
		return nil
	}
	if query == "" {
		return errors.New("target query must not be empty after ?")
	}
	prev := ""
	for i, pair := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || !isQueryKey(key) {
			return errors.New("target query key must match [a-z][a-z0-9_]{0,31}")
		}
		if len(value) > 128 {
			return errors.New("target query value exceeds 128 bytes")
		}
		for j := 0; j < len(value); j++ {
			if !isUnreserved(value[j]) {
				return errors.New("target query value contains a character outside [A-Za-z0-9._~-]")
			}
		}
		if i > 0 && key <= prev {
			return errors.New("target query keys must be strictly ascending and unique")
		}
		prev = key
	}
	return nil
}

// isUnreserved 是 RFC 3986 的 unreserved 字符集。
func isUnreserved(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

func isQueryKey(key string) bool {
	if key == "" || len(key) > 32 || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for i := 1; i < len(key); i++ {
		c := key[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
