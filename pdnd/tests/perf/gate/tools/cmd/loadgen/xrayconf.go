package main

import (
	"encoding/json"
	"fmt"

	"github.com/aegispanel/nodeagent/perfgate/internal/profile"
)

// xrayClientConfig 生成仅含出站 tag "out" 的 xray JSON 客户端配置。
func xrayClientConfig(p *profile.Profile, proto, node string, user profile.User) ([]byte, error) {
	port := p.Port(proto)
	if port == 0 {
		return nil, fmt.Errorf("未知 xray 协议: %s", proto)
	}

	outbound := map[string]any{
		"tag":      "out",
		"protocol": "",
		"settings": map[string]any{},
	}
	stream := map[string]any{
		"network": "tcp",
	}

	switch proto {
	case profile.VLESSVision:
		outbound["protocol"] = "vless"
		outbound["settings"] = map[string]any{
			"vnext": []any{map[string]any{
				"address": node,
				"port":    port,
				"users": []any{map[string]any{
					"id": user.UUID, "encryption": "none", "flow": "xtls-rprx-vision",
				}},
			}},
		}
		stream["network"] = "tcp"
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName":  p.Reality.ServerName,
			"fingerprint": "chrome",
			"publicKey":   p.Reality.PublicKey,
			"shortId":     p.Reality.ShortID,
		}
	case profile.VLESSGRPC:
		outbound["protocol"] = "vless"
		outbound["settings"] = map[string]any{
			"vnext": []any{map[string]any{
				"address": node,
				"port":    port,
				"users": []any{map[string]any{
					"id": user.UUID, "encryption": "none",
				}},
			}},
		}
		stream["network"] = "grpc"
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"serverName":  p.Reality.ServerName,
			"fingerprint": "chrome",
			"publicKey":   p.Reality.PublicKey,
			"shortId":     p.Reality.ShortID,
		}
		stream["grpcSettings"] = map[string]any{
			"serviceName": profile.GRPCService,
		}
	case profile.TrojanTCP:
		outbound["protocol"] = "trojan"
		outbound["settings"] = map[string]any{
			"servers": []any{map[string]any{
				"address": node, "port": port, "password": user.UUID,
			}},
		}
		stream["network"] = "tcp"
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName":           profile.ServerName,
			"fingerprint":          "chrome",
			"pinnedPeerCertSha256": p.Cert.LeafSHA256,
		}
	case profile.TrojanWS:
		outbound["protocol"] = "trojan"
		outbound["settings"] = map[string]any{
			"servers": []any{map[string]any{
				"address": node, "port": port, "password": user.UUID,
			}},
		}
		stream["network"] = "ws"
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName":           profile.ServerName,
			"fingerprint":          "chrome",
			"pinnedPeerCertSha256": p.Cert.LeafSHA256,
		}
		stream["wsSettings"] = map[string]any{
			"path": profile.TrojanWSPath,
			"host": profile.ServerName,
		}
	case profile.SS:
		outbound["protocol"] = "shadowsocks"
		outbound["settings"] = map[string]any{
			"servers": []any{map[string]any{
				"address": node, "port": port,
				"method": profile.SSMethod, "password": user.UUID,
			}},
		}
		stream["network"] = "tcp"
	case profile.VMessWS:
		outbound["protocol"] = "vmess"
		outbound["settings"] = map[string]any{
			"vnext": []any{map[string]any{
				"address": node,
				"port":    port,
				"users": []any{map[string]any{
					"id": user.UUID, "alterId": 0, "security": "auto",
				}},
			}},
		}
		stream["network"] = "ws"
		stream["wsSettings"] = map[string]any{
			"path": profile.VMessWSPath,
			"host": profile.ServerName,
		}
	default:
		return nil, fmt.Errorf("非 xray 协议: %s", proto)
	}

	outbound["streamSettings"] = stream
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": "none"},
		"outbounds": []any{outbound},
		"routing": map[string]any{
			"rules": []any{map[string]any{
				"type": "field", "network": "tcp,udp", "outboundTag": "out",
			}},
		},
	}
	return json.Marshal(cfg)
}
