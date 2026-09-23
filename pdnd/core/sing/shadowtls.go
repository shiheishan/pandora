package sing

// ShadowTLS 的组合入站。
//
// ShadowTLS 与其它协议不是一类东西：它自己不承载流量，只负责把连接伪装成
// 一次到真实网站的 TLS 握手。主动探测去连这个端口，看到的是那个真网站的
// 证书和响应 —— 因为握手确实是转发给它完成的。解开外壳之后，
// 真正的代理流量交给内层协议处理。
//
// 因此一个「ShadowTLS 节点」在 sing-box 里是两个入站：
//
//	tag          shadowtls    对外监听，节点级密码，detour → tag-inner
//	tag-inner    shadowsocks  不对外监听，管用户、算流量
//
// 上层完全不需要知道这件事：AddUsers(tag) 会被路由到内层那个入站，
// 取流量也一样（见 sing.go 里 userTags 的处理）。

import (
	"fmt"
	"strings"

	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json/badoption"

	"net/netip"

	"github.com/aegispanel/nodeagent/core"
)

// shadowTLSInnerTag 是内层入站的 tag。
func shadowTLSInnerTag(tag string) string { return tag + "-inner" }

// buildShadowTLSInner 构造承载真实流量的内层入站。
//
// 内层默认用经典 AEAD（aes-256-gcm）而不是 Shadowsocks 2022，原因是密码格式：
// 2022 要求每个用户的 PSK 是合法 base64 且长度与加密方式严格匹配，
// 而面板给每个用户下发的身份是一个 UUID 字符串 —— 直接当 PSK 会被
// 「decode psk: illegal base64 data」拒掉。经典 AEAD 接受任意字符串做密码，
// 面板那一个 UUID 可以原样用在所有协议上，订阅生成也不必为它开特例。
//
// 安全性上这个取舍是站得住的：外层 ShadowTLS 已经提供了完整的 TLS 伪装与
// 抗主动探测能力，内层加密只需要保证机密性，经典 AEAD 完全够用。
// 真要用 2022 也可以配，但那时面板下发的用户密码必须自己是合法 PSK。
func buildShadowTLSInner(tag string, cfg *core.InboundConfig) (option.Inbound, string, error) {
	innerTag := shadowTLSInnerTag(tag)

	method := "aes-256-gcm"
	if m, ok := cfg.Raw["method"].(string); ok && m != "" {
		method = m
	}

	o := option.ShadowsocksInboundOptions{Method: method}
	if strings.HasPrefix(method, "2022-") {
		// 2022 系列还需要一把服务端根密钥
		serverKey, _ := cfg.Raw["server_key"].(string)
		if serverKey == "" {
			return option.Inbound{}, "", fmt.Errorf("内层 %s 需要 server_key", method)
		}
		o.Password = serverKey
	}

	// 只监听回环，且端口交给内核随机分配。
	//
	// 内层入站的连接全部由 ShadowTLS 通过 detour 注入，不需要任何外部可达性。
	// 绑到 0.0.0.0 会凭空多开一个裸的 Shadowsocks 端口 ——
	// 那等于在一个主打抗探测的协议旁边留了个毫无伪装的后门。
	loopback := badoption.Addr(netip.AddrFrom4([4]byte{127, 0, 0, 1}))
	o.ListenOptions = option.ListenOptions{Listen: &loopback, ListenPort: 0}

	return option.Inbound{Type: C.TypeShadowsocks, Tag: innerTag, Options: &o}, innerTag, nil
}
