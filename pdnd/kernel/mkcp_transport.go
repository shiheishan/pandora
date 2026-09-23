package kernel

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/internal/nativewire/mkcp"
	"github.com/aegispanel/nodeagent/internal/nativewire/udpmask"
)

// mKCP 传输接入。
//
// mKCP 在 UDP 上重建可靠有序的字节流，代价是带宽——它靠激进的重传换低
// 延迟，同样的数据比 TCP 多发不少。值得用的场景是丢包高、RTT 抖动大的
// 链路（跨境移动网络最典型），那里 TCP 的拥塞退让会让速度掉到不可用。
//
// 对上层协议来说它就是一个 net.Listener，和 TCP 没有区别。

// isMKCPNetwork 判断配置里的 network 是不是 mKCP。
//
// 认三种写法：面板里存的是 "mkcp"，而客户端配置文件（v2rayN 之类）
// 导出的是 "kcp"，用户直接把那段贴过来是常事。
func isMKCPNetwork(network string) bool {
	switch strings.ToLower(strings.TrimSpace(network)) {
	case "mkcp", "kcp", "m-kcp":
		return true
	}
	return false
}

// mKCP 参数的取值范围。超出范围的配置一律拒绝而不是夹到边界——
// 悄悄改写用户填的值，会让他在面板上看到的和实际生效的对不上。
const (
	mkcpMinMTU      = 576  // IPv4 要求的最小重组缓冲，再小没有实际意义
	mkcpMaxMTU      = 1460 // 以太网 MTU 1500 减去 IPv4 + UDP 头
	mkcpMinTTI      = 10
	mkcpMaxTTI      = 100
	mkcpMaxCapacity = 1000 // MB/s，够到万兆
)

// ParseMKCPConfig 从节点配置里解析 mKCP 参数。
//
// 字段名跟随 xray 的 kcpSettings，这样从客户端配置直接抄过来能用。
// 支持扁平（面板存的形式）和嵌套在 kcpSettings 里两种写法。
func ParseMKCPConfig(raw map[string]any) (*mkcp.Config, error) {
	// 嵌套优先：显式写了 kcpSettings 说明用户是照着 xray 的格式配的，
	// 那一层里的值应当盖过外面可能残留的同名字段。
	settings := raw
	if nested, ok := rawValue(raw, "kcp_settings").(map[string]any); ok {
		settings = nested
	}

	config := mkcp.DefaultConfig()

	if v := rawValue(settings, "mtu"); v != nil {
		mtu, ok := rawInt(v)
		if !ok {
			return nil, fmt.Errorf("mkcp: mtu 必须是整数")
		}
		if mtu < mkcpMinMTU || mtu > mkcpMaxMTU {
			return nil, fmt.Errorf("mkcp: mtu 应在 %d 到 %d 之间，得到 %d",
				mkcpMinMTU, mkcpMaxMTU, mtu)
		}
		config.MTU = uint32(mtu)
	}

	tti := int(config.Tick / time.Millisecond)
	if v := rawValue(settings, "tti"); v != nil {
		n, ok := rawInt(v)
		if !ok {
			return nil, fmt.Errorf("mkcp: tti 必须是整数")
		}
		if n < mkcpMinTTI || n > mkcpMaxTTI {
			return nil, fmt.Errorf("mkcp: tti 应在 %d 到 %d 毫秒之间，得到 %d",
				mkcpMinTTI, mkcpMaxTTI, n)
		}
		tti = n
		config.Tick = time.Duration(n) * time.Millisecond
	}

	// uplink/downlink 的单位是 MB/s，要换算成「一次能在途多少个段」。
	// 换算式和 xray 一致，否则同样的配置在两边跑出来的窗口大小不同。
	uplink, err := mkcpCapacity(settings, "uplink_capacity", 5)
	if err != nil {
		return nil, err
	}
	downlink, err := mkcpCapacity(settings, "downlink_capacity", 20)
	if err != nil {
		return nil, err
	}
	config.InFlightSize = mkcpInFlight(uplink, config.MTU, uint32(tti))
	config.ReceivingWindowSize = mkcpInFlight(downlink, config.MTU, uint32(tti))

	if v := rawValue(settings, "congestion"); v != nil {
		enabled, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("mkcp: congestion 必须是布尔值")
		}
		config.Congestion = enabled
	}

	// readBufferSize / writeBufferSize 的单位是 MB。
	if v := rawValue(settings, "write_buffer_size"); v != nil {
		mb, ok := rawInt(v)
		if !ok || mb <= 0 {
			return nil, fmt.Errorf("mkcp: writeBufferSize 必须是正整数（单位 MB）")
		}
		config.SendingWindowSize = uint32(mb) * 1024 * 1024 / config.MTU
	}
	if v := rawValue(settings, "read_buffer_size"); v != nil {
		mb, ok := rawInt(v)
		if !ok || mb <= 0 {
			return nil, fmt.Errorf("mkcp: readBufferSize 必须是正整数（单位 MB）")
		}
		config.SocketBuffer = mb * 1024 * 1024
	}

	// seed 和伪装头这两个字段在上游 v1.26 已经从 kcpSettings 里移出去了。
	//
	// 它们没有消失，而是被提到 finalmask 这一层：seed 的加密变成
	// mkcp-aes128gcm，srtp / utp / wechat / dtls / wireguard 变成通用的
	// header-* 掩码，都作用在 UDP 包上而不再是 mKCP 专属。裸 mKCP 是默认，
	// 不配任何掩码两边就能通——上面那些互通测试就是这么跑的。
	//
	// 我们暂时只支持裸的。配了就明确报错，不静默忽略：客户端套了掩码之后
	// 发出来的包我们一个都认不出，表现是「连上了但什么都传不了」，让它在
	// 启动时就失败，比让运维对着一个静默不通的节点查半天强。
	//
	// 将来要支持不用动 mkcp 包——finalmask 作用在 PacketConn 上，
	// mkcp.NewListener 收的正是 net.PacketConn，套一层就行。
	if seed := rawString(settings, "seed"); seed != "" {
		return nil, fmt.Errorf("mkcp: seed 已被上游移除，请改用 finalmask 的 mkcp-aes128gcm（本节点配置里写 mask + mask_password）")
	}
	if header, ok := rawValue(settings, "header").(map[string]any); ok {
		if t := rawString(header, "type"); t != "" && !strings.EqualFold(t, "none") {
			return nil, fmt.Errorf("mkcp: 暂不支持伪装头 %q（上游已迁移为 finalmask 的 header-%s），请改用 none", t, t)
		}
	}

	return config, nil
}

// ParseMKCPMask 从节点配置里读掩码设置。
//
// 认两种写法。扁平的是面板存的形式：
//
//	{"mask": "mkcp-aes128gcm", "mask_password": "..."}
//
// 嵌套的是 xray 的 streamSettings 格式，从客户端配置直接抄过来能用：
//
//	{"finalmask": {"udp": [{"type": "mkcp-aes128gcm",
//	                        "settings": {"password": "..."}}]}}
//
// 上游的 finalmask.udp 是个数组，可以叠好几层。我们只取第一层——多层
// 叠加的收益很小（第二层看到的已经是随机字节了），而每多一层就多一份
// 每包开销和一次拷贝。真需要的时候再说，现在支持了也没人配得对。
func ParseMKCPMask(raw map[string]any) (name, password string, err error) {
	settings := raw
	if nested, ok := rawValue(raw, "kcp_settings").(map[string]any); ok {
		settings = nested
	}

	name = rawString(settings, "mask")
	password = rawString(settings, "mask_password")

	// 嵌套写法优先：显式写了 finalmask 说明是照着上游格式配的。
	if fm, ok := rawValue(raw, "finalmask").(map[string]any); ok {
		udp, _ := rawValue(fm, "udp").([]any)
		if len(udp) > 1 {
			return "", "", fmt.Errorf("mkcp: finalmask.udp 目前只支持一层，收到 %d 层", len(udp))
		}
		if len(udp) == 1 {
			entry, ok := udp[0].(map[string]any)
			if !ok {
				return "", "", fmt.Errorf("mkcp: finalmask.udp[0] 不是对象")
			}
			name = rawString(entry, "type")
			if inner, ok := rawValue(entry, "settings").(map[string]any); ok {
				password = rawString(inner, "password")
			}
		}
	}

	// 提前校验一次，让配错的节点在启动时就失败。放到 Listen 里才发现的话，
	// 错误会混在监听失败里，看不出是掩码的问题。
	if _, err := udpmask.New(name, password); err != nil {
		return "", "", fmt.Errorf("mkcp: %w", err)
	}
	return name, password, nil
}

// mkcpCapacity 读一个以 MB/s 为单位的容量字段。
func mkcpCapacity(settings map[string]any, key string, fallback int) (int, error) {
	v := rawValue(settings, key)
	if v == nil {
		return fallback, nil
	}
	n, ok := rawInt(v)
	if !ok {
		return 0, fmt.Errorf("mkcp: %s 必须是整数", key)
	}
	if n <= 0 || n > mkcpMaxCapacity {
		return 0, fmt.Errorf("mkcp: %s 应在 1 到 %d 之间（单位 MB/s），得到 %d",
			key, mkcpMaxCapacity, n)
	}
	return n, nil
}

// mkcpInFlight 把带宽换算成在途段数。
//
// 一个 TTI 周期内能发多少字节，就是带宽乘以周期；除以 MTU 得到段数。
// 和 xray 的换算保持一致，包括那个下限 8——低于它连基本的流水线都
// 形不成，每个 RTT 只能发几个段。
func mkcpInFlight(capacityMBps int, mtu, ttiMillis uint32) uint32 {
	if ttiMillis == 0 {
		ttiMillis = 50
	}
	size := uint32(capacityMBps) * 1024 * 1024 / mtu / (1000 / ttiMillis)
	if size < 8 {
		size = 8
	}
	return size
}

// ListenMKCP 在给定地址上开一个 mKCP 监听器。
//
// 返回的是标准 net.Listener，上层协议不需要知道底下是 UDP——TLS、
// REALITY 之类都能照常往上套。
//
// 掩码套在 PacketConn 上，位置在 mKCP 之下：mKCP 看到的永远是明文段，
// 线上跑的是掩码后的字节。这个顺序不能反——反过来的话掩码只能盖住
// 载荷，mKCP 的头还是明文，会话号那些特征照样露在外面。
func ListenMKCP(address string, raw map[string]any) (net.Listener, error) {
	config, err := ParseMKCPConfig(raw)
	if err != nil {
		return nil, err
	}
	maskName, maskPassword, err := ParseMKCPMask(raw)
	if err != nil {
		return nil, err
	}
	overhead, err := udpmask.Overhead(maskName, maskPassword)
	if err != nil {
		return nil, err
	}
	if err := checkMKCPMTUFitsMask(config.MTU, overhead); err != nil {
		return nil, err
	}

	packet, err := net.ListenPacket("udp", address)
	if err != nil {
		return nil, err
	}
	tuneMKCPSocketBuffer(packet, config.SocketBuffer)

	masked, err := udpmask.Wrap(packet, maskName, maskPassword)
	if err != nil {
		_ = packet.Close()
		return nil, err
	}
	// 不用 mkcp.Listen：它自己开 socket，掩码就没地方插。
	return mkcp.NewListener(masked, config), nil
}

// tuneMKCPSocketBuffer 放大 UDP socket 收发缓冲。
//
// 必须在套掩码之前对着真的 socket 调——掩码包装之后那层没有
// SetReadBuffer，调了会静默失效，而症状只是「莫名其妙的丢包重传」。
func tuneMKCPSocketBuffer(conn net.PacketConn, size int) {
	if size <= 0 {
		return
	}
	type buffered interface {
		SetReadBuffer(int) error
		SetWriteBuffer(int) error
	}
	if c, ok := conn.(buffered); ok {
		_ = c.SetReadBuffer(size)
		_ = c.SetWriteBuffer(size)
	}
}

// checkMKCPMTUFitsMask 确认加上掩码开销之后的包还能塞进一个以太网帧。
//
// mkcpMaxMTU（1460）是「UDP 载荷」的上限，已经扣掉了 IP + UDP 头。掩码
// 的开销加在载荷里，所以配了掩码之后可用的载荷要再少一截：1460 的 MTU
// 加 28 字节 aes128gcm 开销，实际发出去是 1516 字节的以太网帧，超了。
//
// 超了的后果是 IP 分片，而分片的 UDP 在不少网络上会被直接丢弃。症状是
// 「小包能通、大包不通」——网页打得开、下载一卡就死，最难往传输层上想。
//
// 这里选择报错而不是自动把 MTU 调小。悄悄改写用户填的值，会让他在面板上
// 看到 1460、实际跑着 1432，下次排查时对不上账。默认值 1350 离上限还远，
// 只有手动调过 MTU 的人会撞到这条，而那种人看得懂这个错误。
func checkMKCPMTUFitsMask(mtu uint32, overhead int) error {
	if overhead == 0 {
		return nil
	}
	limit := uint32(mkcpMaxMTU - overhead)
	if mtu > limit {
		return fmt.Errorf(
			"mkcp: 配了掩码之后 mtu 不能超过 %d（掩码每包多占 %d 字节，%d 会让加密后的包被 IP 分片），当前 %d",
			limit, overhead, mtu, mtu)
	}
	return nil
}
