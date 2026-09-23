package kernel

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// RealityClientConfig is the stable client-side contract used by the panel
// and the future Pandora native REALITY handshake. It intentionally contains
// only wire parameters; no xray/sing runtime object is exposed here.
type RealityClientConfig struct {
	ServerName  string
	PublicKey   []byte
	ShortID     [8]byte
	Fingerprint string
	SpiderX     string
}

// ParseRealityClientConfig validates the panel's client-side REALITY fields.
// public_key/password accepts the standard 32-byte base64url key forms used
// by Xray clients; short_id is an even-length hexadecimal value up to 16
// characters and is zero-padded to the protocol's eight-byte field.
func ParseRealityClientConfig(raw map[string]any) (RealityClientConfig, error) {
	var out RealityClientConfig
	out.ServerName = strings.TrimSpace(rawString(raw, "server_name"))
	if out.ServerName == "" || strings.ContainsAny(out.ServerName, " /\\\r\n") {
		return out, fmt.Errorf("reality client server_name 无效")
	}
	keyText := rawString(raw, "public_key")
	if keyText == "" {
		keyText = rawString(raw, "password")
	}
	key, err := decodeKey(keyText)
	if err != nil {
		return out, fmt.Errorf("reality client public_key: %w", err)
	}
	out.PublicKey = key
	shortID := strings.TrimSpace(rawString(raw, "short_id"))
	if len(shortID) > 16 || len(shortID)%2 != 0 {
		return out, fmt.Errorf("reality client short_id 必须是不超过 16 位的偶数位十六进制")
	}
	if shortID != "" {
		decoded, decodeErr := hex.DecodeString(shortID)
		if decodeErr != nil {
			return out, fmt.Errorf("reality client short_id 不是十六进制: %w", decodeErr)
		}
		copy(out.ShortID[:], decoded)
	}
	out.Fingerprint = strings.ToLower(strings.TrimSpace(rawString(raw, "fingerprint")))
	if out.Fingerprint == "" {
		out.Fingerprint = "chrome"
	}
	out.SpiderX = strings.TrimSpace(rawString(raw, "spider_x"))
	if out.SpiderX != "" {
		if _, err := url.Parse(out.SpiderX); err != nil {
			return out, fmt.Errorf("reality client spider_x: %w", err)
		}
	}
	return out, nil
}
