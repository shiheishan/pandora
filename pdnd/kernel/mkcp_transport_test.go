package kernel

import (
	"strings"
	"testing"
	"time"
)

func TestIsMKCPNetwork(t *testing.T) {
	// "kcp" 也要认：面板里存的是 mkcp，但客户端配置文件导出的是 kcp，
	// 用户直接把那段贴过来是常事。
	for _, name := range []string{"mkcp", "kcp", "MKCP", " kcp ", "m-kcp"} {
		if !isMKCPNetwork(name) {
			t.Errorf("%q 没被认成 mKCP", name)
		}
	}
	for _, name := range []string{"", "tcp", "ws", "grpc", "quic", "kcpx"} {
		if isMKCPNetwork(name) {
			t.Errorf("%q 被误认成 mKCP", name)
		}
	}
}

func TestParseMKCPConfigDefaults(t *testing.T) {
	config, err := ParseMKCPConfig(map[string]any{"network": "mkcp"})
	if err != nil {
		t.Fatal(err)
	}
	if config.MTU != 1350 {
		t.Errorf("MTU = %d，期望 1350", config.MTU)
	}
	if config.Tick != 50*time.Millisecond {
		t.Errorf("Tick = %v，期望 50ms", config.Tick)
	}
	// 默认 uplink 5 MB/s：5*1024*1024/1350/(1000/50) = 194
	if config.InFlightSize != 194 {
		t.Errorf("InFlightSize = %d，期望 194", config.InFlightSize)
	}
	// 默认 downlink 20 MB/s
	if config.ReceivingWindowSize != 776 {
		t.Errorf("ReceivingWindowSize = %d，期望 776", config.ReceivingWindowSize)
	}
}

func TestParseMKCPConfigReadsSettings(t *testing.T) {
	config, err := ParseMKCPConfig(map[string]any{
		"mtu":               1200,
		"tti":               20,
		"uplink_capacity":   10,
		"downlink_capacity": 50,
		"congestion":        true,
		"write_buffer_size": 4,
		"read_buffer_size":  8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.MTU != 1200 {
		t.Errorf("MTU = %d", config.MTU)
	}
	if config.Tick != 20*time.Millisecond {
		t.Errorf("Tick = %v", config.Tick)
	}
	if !config.Congestion {
		t.Error("congestion 没生效")
	}
	if config.SocketBuffer != 8*1024*1024 {
		t.Errorf("SocketBuffer = %d", config.SocketBuffer)
	}
	if config.SendingWindowSize != 4*1024*1024/1200 {
		t.Errorf("SendingWindowSize = %d", config.SendingWindowSize)
	}
}

// 面板存扁平字段，客户端配置是嵌套在 kcpSettings 里的。两种都得认。
func TestParseMKCPConfigAcceptsNestedSettings(t *testing.T) {
	config, err := ParseMKCPConfig(map[string]any{
		"network": "mkcp",
		"kcpSettings": map[string]any{
			"mtu": 1400, "tti": 30, "congestion": true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.MTU != 1400 {
		t.Errorf("MTU = %d，期望从 kcpSettings 里读到 1400", config.MTU)
	}
	if config.Tick != 30*time.Millisecond {
		t.Errorf("Tick = %v", config.Tick)
	}
	if !config.Congestion {
		t.Error("嵌套里的 congestion 没生效")
	}
}

// 越界的值要拒绝，不能悄悄夹到边界——用户在面板上看到的和实际生效的
// 对不上，比直接报错难查得多。
func TestParseMKCPConfigRejectsOutOfRange(t *testing.T) {
	for name, raw := range map[string]map[string]any{
		"mtu 太小":          {"mtu": 100},
		"mtu 太大":          {"mtu": 9000},
		"tti 太小":          {"tti": 1},
		"tti 太大":          {"tti": 5000},
		"uplink 为零":       {"uplink_capacity": 0},
		"downlink 负":      {"downlink_capacity": -5},
		"mtu 不是数字":        {"mtu": "big"},
		"congestion 不是布尔": {"congestion": "yes"},
	} {
		if _, err := ParseMKCPConfig(raw); err == nil {
			t.Errorf("%s：应当报错却通过了", name)
		}
	}
}

// seed 和伪装头我们暂不支持。必须明确报错——静默忽略的话，客户端套了
// 掩码发来的包我们一个都认不出，表现是「连上了但什么都传不了」。
func TestParseMKCPConfigRejectsUnsupportedMasking(t *testing.T) {
	if _, err := ParseMKCPConfig(map[string]any{"seed": "hunter2"}); err == nil {
		t.Error("配了 seed 却没报错")
	} else if !strings.Contains(err.Error(), "finalmask") {
		t.Errorf("错误信息没指出去处：%v", err)
	}

	if _, err := ParseMKCPConfig(map[string]any{
		"header": map[string]any{"type": "srtp"},
	}); err == nil {
		t.Error("配了伪装头却没报错")
	}

	// none 是我们支持的那种，不该被拦
	if _, err := ParseMKCPConfig(map[string]any{
		"header": map[string]any{"type": "none"},
	}); err != nil {
		t.Errorf("header none 被误拒：%v", err)
	}
}

// 换算式必须和 xray 一致，否则同样的配置在两边跑出来的窗口大小不同。
func TestMKCPInFlightMatchesUpstreamFormula(t *testing.T) {
	for _, tc := range []struct {
		capacity int
		mtu, tti uint32
		want     uint32
	}{
		{5, 1350, 50, 194},
		{20, 1350, 50, 776},
		{1, 1350, 100, 77},
		{1, 1460, 10, 8},      // 算出来是 7，不足下限，抬到 8
		{100, 1350, 50, 3883}, // 逐步整数除法，不是一次算完再取整
	} {
		if got := mkcpInFlight(tc.capacity, tc.mtu, tc.tti); got != tc.want {
			t.Errorf("mkcpInFlight(%d, %d, %d) = %d，期望 %d",
				tc.capacity, tc.mtu, tc.tti, got, tc.want)
		}
	}
}

// 掩码的扁平写法（面板存的形式）。
func TestParseMKCPMaskFlat(t *testing.T) {
	name, pw, err := ParseMKCPMask(map[string]any{
		"network": "mkcp", "mask": "mkcp-aes128gcm", "mask_password": "hunter2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "mkcp-aes128gcm" || pw != "hunter2" {
		t.Errorf("解析出 %q / %q", name, pw)
	}
}

// 嵌套写法：xray 的 streamSettings.finalmask，从客户端配置直接抄过来。
func TestParseMKCPMaskNestedFinalmask(t *testing.T) {
	name, pw, err := ParseMKCPMask(map[string]any{
		"network": "mkcp",
		"finalmask": map[string]any{
			"udp": []any{map[string]any{
				"type":     "mkcp-aes128gcm",
				"settings": map[string]any{"password": "from-xray-config"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if name != "mkcp-aes128gcm" || pw != "from-xray-config" {
		t.Errorf("解析出 %q / %q", name, pw)
	}
}

// 不配掩码是合法的——裸 mKCP 仍然能用，只是没有伪装。
func TestParseMKCPMaskEmptyIsAllowed(t *testing.T) {
	for _, raw := range []map[string]any{
		{"network": "mkcp"},
		{"network": "mkcp", "mask": ""},
		{"network": "mkcp", "mask": "none"},
		{"network": "mkcp", "mask": "mkcp-original"},
	} {
		name, _, err := ParseMKCPMask(raw)
		if err != nil {
			t.Errorf("%v 应当合法，却报错 %v", raw, err)
		}
		_ = name
	}
}

// 配错要在启动时就失败，不能等到「连上了但传不了数据」。
func TestParseMKCPMaskRejectsBadConfig(t *testing.T) {
	// 加密掩码不给密码：密钥会退化成 sha256("")，谁都算得出来
	if _, _, err := ParseMKCPMask(map[string]any{"mask": "mkcp-aes128gcm"}); err == nil {
		t.Error("aes128gcm 不给密码却通过了")
	}
	// 不认识的类型不能静默忽略——那会让节点裸奔而管理员以为加着密
	if _, _, err := ParseMKCPMask(map[string]any{"mask": "header-srtp"}); err == nil {
		t.Error("未知掩码类型却通过了")
	}
	// 多层叠加暂不支持，要明说
	if _, _, err := ParseMKCPMask(map[string]any{
		"finalmask": map[string]any{"udp": []any{
			map[string]any{"type": "mkcp-aes128gcm", "settings": map[string]any{"password": "a"}},
			map[string]any{"type": "mkcp-aes128gcm", "settings": map[string]any{"password": "b"}},
		}},
	}); err == nil {
		t.Error("两层掩码却通过了")
	}
}

// 配了掩码之后 MTU 上限要收紧。
//
// mkcpMaxMTU 是 UDP 载荷上限，已经扣过 IP + UDP 头；掩码开销也加在载荷
// 里，所以顶着上限配 MTU 再套掩码就会让以太网帧超长，被 IP 分片。分片的
// UDP 在不少网络上被直接丢弃，症状是「小包能通、大包不通」。
func TestMKCPMTUMustLeaveRoomForMask(t *testing.T) {
	const aesOverhead = 28 // 12 nonce + 16 tag

	// 不配掩码时顶格是允许的
	if err := checkMKCPMTUFitsMask(mkcpMaxMTU, 0); err != nil {
		t.Errorf("没有掩码时 mtu=%d 被拒：%v", mkcpMaxMTU, err)
	}
	// 配了掩码，顶格就该被拒
	if err := checkMKCPMTUFitsMask(mkcpMaxMTU, aesOverhead); err == nil {
		t.Errorf("mtu=%d 加 %d 字节掩码开销会分片，却通过了", mkcpMaxMTU, aesOverhead)
	}
	// 恰好卡在新上限上要放行
	if err := checkMKCPMTUFitsMask(mkcpMaxMTU-aesOverhead, aesOverhead); err != nil {
		t.Errorf("mtu=%d 恰好在上限内却被拒：%v", mkcpMaxMTU-aesOverhead, err)
	}
	// 默认值离上限还很远，不该受影响
	if err := checkMKCPMTUFitsMask(1350, aesOverhead); err != nil {
		t.Errorf("默认 mtu=1350 被拒：%v", err)
	}

	// 算给人看的：收紧后的上限加上开销和 IP/UDP 头，正好不超以太网帧
	const ethernetMTU, ipUDPHeader = 1500, 28
	if (mkcpMaxMTU-aesOverhead)+aesOverhead+ipUDPHeader > ethernetMTU {
		t.Error("收紧后的上限仍会让包分片")
	}
}
