package kernel

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strings"
)

// maxInboundECHKeys 是入站允许配置的最大 ECH 密钥数。
const maxInboundECHKeys = 8

// inboundECHKeys 解析 raw["ech_keys"] 为标准库 tls.EncryptedClientHelloKey 切片。
func inboundECHKeys(raw map[string]any) ([]tls.EncryptedClientHelloKey, error) {
	if raw == nil {
		return nil, nil
	}
	val := rawValue(raw, "ech_keys")
	if val == nil {
		return nil, nil
	}
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("ech_keys must be an array")
	}
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxInboundECHKeys {
		return nil, fmt.Errorf("ech_keys exceeds maximum count of %d", maxInboundECHKeys)
	}

	keys := make([]tls.EncryptedClientHelloKey, 0, len(items))
	for i, itemRaw := range items {
		item, ok := itemRaw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ech_keys[%d]: must be an object", i)
		}

		configVal, ok := rawValue(item, "config").(string)
		if !ok || strings.TrimSpace(configVal) == "" {
			return nil, fmt.Errorf("ech_keys[%d]: config must be a non-empty string", i)
		}
		cfgBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(configVal))
		if err != nil {
			return nil, fmt.Errorf("ech_keys[%d]: config base64 decode: %w", i, err)
		}
		if len(cfgBytes) < 4 {
			return nil, fmt.Errorf("ech_keys[%d]: config is too short", i)
		}
		if binary.BigEndian.Uint16(cfgBytes[0:2]) != 0xfe0d {
			return nil, fmt.Errorf("ech_keys[%d]: config version must be 0xfe0d", i)
		}
		contentLen := int(binary.BigEndian.Uint16(cfgBytes[2:4]))
		if contentLen != len(cfgBytes)-4 {
			return nil, fmt.Errorf("ech_keys[%d]: config length mismatch", i)
		}

		privVal, ok := rawValue(item, "private_key").(string)
		if !ok || strings.TrimSpace(privVal) == "" {
			return nil, fmt.Errorf("ech_keys[%d]: private_key must be a non-empty string", i)
		}
		privBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privVal))
		if err != nil {
			return nil, fmt.Errorf("ech_keys[%d]: private_key base64 decode: %w", i, err)
		}
		if len(privBytes) != 32 {
			return nil, fmt.Errorf("ech_keys[%d]: private_key must be 32 bytes, got %d", i, len(privBytes))
		}

		var sendAsRetry bool
		if retryRaw := rawValue(item, "send_as_retry"); retryRaw != nil {
			b, ok := retryRaw.(bool)
			if !ok {
				return nil, fmt.Errorf("ech_keys[%d]: send_as_retry must be a boolean", i)
			}
			sendAsRetry = b
		}

		keys = append(keys, tls.EncryptedClientHelloKey{
			Config:      cfgBytes,
			PrivateKey:  privBytes,
			SendAsRetry: sendAsRetry,
		})
	}
	return keys, nil
}

// applyInboundECH 尝试解析 ech_keys 并装配进 tls.Config。
// 开了 ECH 的节点只接受 TLS 1.3（Go 标准库要求配置了 EncryptedClientHelloKeys 时 MinVersion 若设置必须是 TLS 1.3）。
func applyInboundECH(cfg *tls.Config, raw map[string]any) error {
	keys, err := inboundECHKeys(raw)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	cfg.EncryptedClientHelloKeys = keys
	cfg.MinVersion = tls.VersionTLS13
	return nil
}
