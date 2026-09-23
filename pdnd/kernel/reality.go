package kernel

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

// RealityServerConfig is Pandora's protocol-level REALITY configuration. It
// intentionally contains no xray Instance, router, or statistics objects.
type RealityServerConfig struct {
	Dest        string
	ServerNames map[string]bool
	PrivateKey  []byte
	ShortIDs    map[[8]byte]bool
	MaxTimeDiff time.Duration
	Xver        byte
}

func ParseRealityServerConfig(raw map[string]any) (RealityServerConfig, error) {
	var out RealityServerConfig
	out.Dest = strings.TrimSpace(stringValue(raw["dest"]))
	if out.Dest == "" {
		return out, fmt.Errorf("reality 需要 dest")
	}
	if _, _, err := net.SplitHostPort(out.Dest); err != nil {
		return out, fmt.Errorf("reality dest 必须是 host:port: %w", err)
	}
	names, err := stringList(raw["server_names"])
	if err != nil || len(names) == 0 {
		if err != nil {
			return out, fmt.Errorf("reality server_names: %w", err)
		}
		return out, fmt.Errorf("reality 至少需要一个 server_name")
	}
	out.ServerNames = make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
		if name == "" || strings.ContainsAny(name, " /\\") {
			return out, fmt.Errorf("reality server_name %q 无效", name)
		}
		out.ServerNames[name] = true
	}
	privateKey, err := decodeKey(stringValue(raw["private_key"]))
	if err != nil {
		return out, fmt.Errorf("reality private_key: %w", err)
	}
	out.PrivateKey = privateKey
	shortIDs, err := parseShortIDs(raw["short_ids"])
	if err != nil {
		return out, err
	}
	out.ShortIDs = shortIDs
	if rawValue, ok := raw["max_time_diff"]; ok {
		out.MaxTimeDiff, err = parseDurationSeconds(rawValue)
		if err != nil {
			return out, fmt.Errorf("reality max_time_diff: %w", err)
		}
		if out.MaxTimeDiff > 10*time.Minute {
			return out, fmt.Errorf("reality max_time_diff 不能超过 10 分钟")
		}
	}
	if rawValue, ok := raw["xver"]; ok {
		xver, parseErr := parseRealityXver(rawValue)
		if parseErr != nil {
			return out, parseErr
		}
		out.Xver = xver
	}
	return out, nil
}

func parseRealityXver(value any) (byte, error) {
	switch v := value.(type) {
	case string:
		parsed, err := strconv.ParseUint(strings.TrimSpace(v), 10, 8)
		if err != nil || parsed > 2 {
			return 0, fmt.Errorf("reality xver 必须是 0、1 或 2")
		}
		return byte(parsed), nil
	case float64:
		if v != math.Trunc(v) || v < 0 || v > 2 {
			return 0, fmt.Errorf("reality xver 必须是 0、1 或 2")
		}
		return byte(v), nil
	case int:
		if v < 0 || v > 2 {
			return 0, fmt.Errorf("reality xver 必须是 0、1 或 2")
		}
		return byte(v), nil
	case nil:
		return 0, nil
	default:
		return 0, fmt.Errorf("reality xver 必须是数字或字符串")
	}
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func stringList(v any) ([]string, error) {
	switch value := v.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return nil, nil
		}
		return []string{value}, nil
	case []string:
		return append([]string(nil), value...), nil
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("列表包含非字符串或空值")
			}
			out = append(out, s)
		}
		return out, nil
	default:
		if v == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("必须是字符串或字符串列表")
	}
}

func decodeKey(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("不能为空")
	}
	for _, encoding := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if decoded, err := encoding.DecodeString(value); err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("必须是 32 字节 base64url 私钥")
}

func parseShortIDs(v any) (map[[8]byte]bool, error) {
	values, err := stringList(v)
	if err != nil {
		return nil, fmt.Errorf("reality short_ids: %w", err)
	}
	if len(values) == 0 {
		values = []string{""}
	}
	out := make(map[[8]byte]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if len(value) > 16 || len(value)%2 != 0 {
			return nil, fmt.Errorf("reality short_id %q 必须是不超过 16 位的偶数位十六进制", value)
		}
		decoded, err := hex.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("reality short_id %q 不是十六进制", value)
		}
		var id [8]byte
		copy(id[:], decoded)
		out[id] = true
	}
	return out, nil
}

func parseDurationSeconds(v any) (time.Duration, error) {
	if text, ok := v.(string); ok {
		d, err := time.ParseDuration(strings.TrimSpace(text))
		if err != nil || d < 0 {
			return 0, fmt.Errorf("必须是非负秒数或 duration")
		}
		return d, nil
	}
	seconds, ok := v.(float64)
	if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || math.Trunc(seconds) != seconds {
		return 0, fmt.Errorf("必须是非负整数秒")
	}
	return time.Duration(seconds) * time.Second, nil
}
