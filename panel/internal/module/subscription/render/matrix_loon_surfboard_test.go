package render

import "testing"

type loonSurfboardRow struct {
	loon, surfboard cell
}

func loonSurfboardMatrix() map[string]loonSurfboardRow {
	pub := fixtureRealityPub
	return map[string]loonSurfboardRow{
		"ss-aes128": {
			loon:      has("= Shadowsocks,node.example.com,", "aes-128-gcm", "udp=true"),
			surfboard: has("= ss, node.example.com, 8388", "encrypt-method=aes-128-gcm", "udp-relay=true"),
		},
		"ss-chacha": {
			loon:      has("chacha20-ietf-poly1305", "udp=true"),
			surfboard: has("encrypt-method=chacha20-ietf-poly1305", "udp-relay=true"),
		},
		// Loon 文档的 2022 示例就是 iPSK:uPSK 冒号写法；Surfboard 文档要求 2022 密码是单个定长 base64，认不了多用户
		"ss2022-aes128": {
			loon:      has("= Shadowsocks,node.example.com,8390,2022-blake3-aes-128-gcm,", `"`+ss2022PW128+`"`, "udp=true"),
			surfboard: skip("Surfboard 不支持 Shadowsocks 2022"),
		},
		"ss2022-aes256": {
			loon:      has("= Shadowsocks,node.example.com,8391,2022-blake3-aes-256-gcm,", `"`+ss2022PW256+`"`, "udp=true"),
			surfboard: skip("Surfboard 不支持 Shadowsocks 2022"),
		},
		"ss2022-chacha": {
			loon:      skip("多用户不支持"),
			surfboard: skip("Surfboard 不支持 Shadowsocks 2022"),
		},
		"hy2-obfs": {
			loon:      has("= Hysteria2,", "salamander-password=obfs-pass-1", "sni=sni.example.com", "skip-cert-verify=true", "udp=true"),
			surfboard: has("= hysteria2,", "salamander-password=obfs-pass-1", "sni=sni.example.com", "skip-cert-verify=true", "udp-relay=true"),
		},
		"hy2-plain": {
			loon:      has("= Hysteria2,").without("salamander", "skip-cert-verify"),
			surfboard: has("= hysteria2,").without("salamander", "skip-cert-verify"),
		},
		"tuic": {
			loon:      skip("Loon 没有 tuic"),
			surfboard: has("= tuic-v5,", "uuid="+fixtureUUID, "alpn=h3", "sni=sni.example.com", "udp-relay=true"),
		},
		"anytls": {
			loon:      has("= AnyTLS,", "sni=sni.example.com", "skip-cert-verify=true", "tls-profile=chrome", "udp=true"),
			surfboard: has("= anytls,", "sni=sni.example.com", "skip-cert-verify=true"),
		},
		"anytls-utls-fallback": {
			loon:      has("tls-profile=safari-ios18").without("decoy"),
			surfboard: has("= anytls,").without("decoy"),
		},
		"naive": {
			loon:      skip("Loon 没有 naive"),
			surfboard: skip("Surfboard 没有 naive"),
		},
		"naive-insecure": {
			loon:      skip("Loon 没有 naive"),
			surfboard: skip("Surfboard 没有 naive"),
		},
		"naive-fallback": {
			loon:      skip("Loon 没有 naive"),
			surfboard: skip("Surfboard 没有 naive"),
		},
		"juicity": {
			loon:      skip("Loon 没有 juicity"),
			surfboard: skip("Surfboard 没有 juicity"),
		},
		"socks": {
			loon:      has("= socks5,", "udp=false"),
			surfboard: has("= socks5,", "udp-relay=false"),
		},
		"socks-udp": {
			loon:      has("udp=true"),
			surfboard: has("udp-relay=true"),
		},
		"socks-tls": {
			loon:      has("over-tls=true"),
			surfboard: has("= socks5-tls,"),
		},
		"http": {
			loon:      has("= http,node.example.com,8080,"),
			surfboard: has("= http, node.example.com, 8080,"),
		},
		"http-tls": {
			loon:      has("= https,"),
			surfboard: has("= https, node.example.com,"),
		},
		"mieru-tcp": {
			loon:      skip("Loon 没有 mieru"),
			surfboard: skip("Surfboard 没有 mieru"),
		},
		"mieru-udp": {
			loon:      skip("Loon 没有 mieru"),
			surfboard: skip("Surfboard 没有 mieru"),
		},
		"shadowtls": {
			loon:      has("= Shadowsocks,", "shadow-tls-password=", "shadow-tls-sni=www.example.com", "shadow-tls-version=3", "udp=false"),
			surfboard: skip("Surfboard 没有 shadowtls"),
		},
		"trojan-tls-tcp": {
			// Loon 的 tls-profile 只写认得的 chrome / safari-ios18（loonTLSProfile），firefox 指纹不写，免得编一个 Loon 不认的值
			loon:      has("= Trojan,", "transport=tcp", "sni=sni.example.com", "skip-cert-verify=true", "udp=true").without("tls-profile"),
			surfboard: has("= trojan,", "password="+fixtureUUID, "sni=sni.example.com", "skip-cert-verify=true", "udp-relay=true"),
		},
		"trojan-tls-ws": {
			loon:      has("transport=ws", "path=/tw", "host=cdn.example.com", "tls-profile=chrome"),
			surfboard: has("ws=true", "ws-path=/tw", "ws-headers=Host:cdn.example.com", "tls=true"),
		},
		"trojan-tls-grpc": {
			loon:      skip("Loon 不支持 grpc"),
			surfboard: skip("Surfboard 不支持 grpc"),
		},
		"trojan-tls-grpc-sni": {
			loon:      skip("grpc"),
			surfboard: skip("grpc"),
		},
		"trojan-tls-httpupgrade": {
			loon:      skip("httpupgrade"),
			surfboard: skip("httpupgrade"),
		},
		"trojan-tls-fallback": {
			loon:      has("= Trojan,", "tls-profile=chrome").without("decoy"),
			surfboard: has("= trojan,").without("decoy"),
		},
		"trojan-reality": {
			loon:      has("= Trojan,", `public-key="`+pub+`"`, "short-id=0a1b2c3d", "sni=www.example.com").without("tls-profile"),
			surfboard: skip("REALITY"),
		},
		"trojan-reality-fallback": {
			loon:      has(`public-key="` + pub + `"`).without("decoy"),
			surfboard: skip("REALITY"),
		},
		"vless-reality-vision": {
			loon:      has("= VLESS,", `public-key="`+pub+`"`, "flow=xtls-rprx-vision", "over-tls=true", "sni=www.example.com", "tls-profile=chrome"),
			surfboard: skip("vless"),
		},
		"vless-reality-multi": {
			loon:      has(`public-key="`+pub+`"`, "short-id=", "flow=xtls-rprx-vision"),
			surfboard: skip("vless"),
		},
		"vless-reality-grpc": {
			loon:      skip("grpc"),
			surfboard: skip("vless"),
		},
		"vless-reality-xhttp": {
			loon:      skip("xhttp"),
			surfboard: skip("vless"),
		},
		"vless-xhttp-header": {
			loon:      skip("session_placement"),
			surfboard: skip("vless"),
		},
		"vless-ws": {
			loon:      has("= VLESS,", "transport=ws", "path=/vw", "host=cdn.example.com", "over-tls=false"),
			surfboard: skip("vless"),
		},
		"vless-ws-nohost": {
			loon:      has("host=node.example.com"),
			surfboard: skip("vless"),
		},
		"vless-httpupgrade": {
			loon:      skip("httpupgrade"),
			surfboard: skip("vless"),
		},
		"vless-grpc": {
			loon:      skip("grpc"),
			surfboard: skip("vless"),
		},
		"vless-grpc-cdnhost": {
			loon:      skip("grpc"),
			surfboard: skip("vless"),
		},
		"vless-mkcp": {
			loon:      skip("mkcp"),
			surfboard: skip("vless"),
		},
		"vless-mkcp-mask": {
			loon:      skip("mkcp"),
			surfboard: skip("vless"),
		},
		"vmess-tcp": {
			loon:      has("= VMess,", "transport=tcp", "alterId=0", "over-tls=false", "udp=true"),
			surfboard: has("= vmess,", "username="+fixtureUUID, "vmess-aead=true", "udp-relay=true"),
		},
		"vmess-ws": {
			loon:      has("transport=ws", "path=/mw", "host=cdn.example.com"),
			surfboard: has("ws-path=/mw", "ws-headers=Host:cdn.example.com"),
		},
		"vmess-grpc": {
			loon:      skip("grpc"),
			surfboard: skip("grpc"),
		},
		"vmess-httpupgrade": {
			loon:      skip("httpupgrade"),
			surfboard: skip("httpupgrade"),
		},
		"vmess-xhttp": {
			loon:      skip("xhttp"),
			surfboard: skip("xhttp"),
		},
		"vless-tcp-plain": {
			loon:      has("= VLESS,", "transport=tcp", "over-tls=false"),
			surfboard: skip("vless"),
		},
		"vless-reality-xhttp-h3": {
			loon:      skip("xhttp-h3"),
			surfboard: skip("vless"),
		},
		"trojan-legacy-cert": {
			loon:      has("= Trojan,", "tls-profile=chrome"),
			surfboard: has("= trojan,", "tls=true"),
		},
	}
}

func TestLoonSurfboardMatrixCoversEveryFormFixture(t *testing.T) {
	matrix := loonSurfboardMatrix()
	fixtures := loadFixtures(t)
	nodes := make([]Node, 0, len(fixtures))
	for _, f := range fixtures {
		nodes = append(nodes, f.toNode())
	}
	nodes = uniqueNodeNames(nodes)
	rows := 0
	for i, n := range nodes {
		if g := fixtures[i].Group; g != groupForm && g != groupLegacy {
			continue
		}
		rows++
		row, ok := matrix[n.Name]
		if !ok {
			t.Errorf("%s: no matrix row", n.Name)
			continue
		}
		loonLine, loonSkip := nodeToLoon(n, fixtureUUID)
		checkCell(t, n.Name, "loon", row.loon, loonLine, loonSkip)
		sbLine, sbSkip := nodeToSurfboard(n, fixtureUUID)
		checkCell(t, n.Name, "surfboard", row.surfboard, sbLine, sbSkip)
	}
	if rows != len(matrix) {
		t.Fatalf("matrix has %d rows, form+legacy fixtures %d", len(matrix), rows)
	}
}
