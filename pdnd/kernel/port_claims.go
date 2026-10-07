package kernel

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"syscall"

	"github.com/aegispanel/nodeagent/core"
)

// ============================================================
//  进程级端口登记：同机端口先到先得
// ============================================================
//
// 一个进程承载这台机器上的全部节点。两个入站配到同一个 (端口, L4) 时，原先
// 由 goroutine 竞速决定谁 bind 成功，每次重启赢家可能换人，输家的用户全部连
// 不上、面板毫无察觉。现在由 NativeCore 统一登记：
//
//   - 键是 (端口, L4 协议)。TCP 与 UDP 分开登记，同一个端口号可以 TCP、UDP
//     各被一个入站占用（例如 REALITY 走 TCP、hy2 走 UDP），这是合法用法。
//   - 已经占着端口的入站一直保留，撞上来的那个不启动，返回 *PortInUseError；
//     占着的那个不会被先关再起。
//   - 同一个入站改端口：先登记新端口，起来之后才释放旧端口；新端口被占就保留
//     旧入站（PreviousPreserved）。
//
// 冷启动谁先谁后由节点端按 config.json 的 nodes[] 顺序串行首装决定（见
// node.StartupOrder），这里只保证「先登记者赢、不抢」。

// portKey 是登记表的键。l4 只取 "tcp" / "udp"。
type portKey struct {
	port int
	l4   string
}

func (k portKey) String() string { return strconv.Itoa(k.port) + "/" + strings.ToUpper(k.l4) }

// inboundPortKey 推导一个入站实际 bind 的 (端口, L4)，规则与各适配器的
// Start 一致（audit-multinode 第 2 节 G3）：
//   - hysteria2、tuic、juicity 走 UDP；
//   - vless / vmess 的 network 为 xhttp-h3 或 mKCP 走 UDP，trojan 的 mKCP 走 UDP；
//   - shadowsocks（含 2022）只有 network=udp 时走 UDP；
//   - mieru 看 transport（缺省 TCP）；
//   - 其余（anytls、socks、http、naive、shadowtls 与上述的 TCP 传输）走 TCP。
//     SOCKS 的 UDP ASSOCIATE 用的是临时端口，不占入站端口。
func inboundPortKey(cfg *core.InboundConfig) portKey {
	key := portKey{port: cfg.Port, l4: "tcp"}
	network, _ := cfg.Raw["network"].(string)
	network = strings.ToLower(strings.TrimSpace(network))
	switch strings.ToLower(strings.TrimSpace(cfg.Protocol)) {
	case "hysteria2", "hy2", "tuic", "juicity":
		key.l4 = "udp"
	case "vless", "vmess":
		if network == "xhttp-h3" || isMKCPNetwork(network) {
			key.l4 = "udp"
		}
	case "trojan":
		if isMKCPNetwork(network) {
			key.l4 = "udp"
		}
	case "shadowsocks", "ss":
		if network == "udp" {
			key.l4 = "udp"
		}
	case "mieru":
		if strings.EqualFold(rawString(cfg.Raw, "transport"), "udp") {
			key.l4 = "udp"
		}
	}
	return key
}

// PortInUseError 是「端口已被占用」：Owner 是占着它的入站 tag（<协议>-<节点 ID>），
// 为空表示占用者不是本进程的入站（bind 返回 EADDRINUSE）。
type PortInUseError struct {
	Port  int
	L4    string // "tcp" / "udp"
	Owner string
	Err   error // 外部占用时 bind 的原始错误
}

func (e *PortInUseError) Error() string {
	key := portKey{port: e.Port, l4: e.L4}.String()
	if e.Owner == "" {
		return "端口 " + key + " 已被本机其他进程占用"
	}
	return "端口 " + key + " 已被节点 " + e.Owner + " 占用"
}

func (e *PortInUseError) Unwrap() error { return e.Err }

// RuntimeReason 是给面板的机器可读原因（纯 ASCII，可放进请求头）：
//
//	port_in_use:<端口>/<tcp|udp>:<占用者 tag>   占用者是同进程的另一个入站
//	port_in_use:<端口>/<tcp|udp>:external      占用者是本机其他进程
//
// 节点端经 errors.As 取这个方法（不 import kernel），随 degraded 心跳上报。
func (e *PortInUseError) RuntimeReason() string {
	owner := e.Owner
	if owner == "" {
		owner = "external"
	}
	return fmt.Sprintf("port_in_use:%d/%s:%s", e.Port, e.L4, owner)
}

// asExternalPortInUse 把 bind 的 EADDRINUSE 包成 PortInUseError；其它错误原样返回。
func asExternalPortInUse(key portKey, err error) error {
	if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
		return err
	}
	var already *PortInUseError
	if errors.As(err, &already) {
		return err
	}
	return &PortInUseError{Port: key.port, L4: key.l4, Err: err}
}

// portOwnerLocked 返回占着 key 的入站 tag；调用方持有 c.mu。
func (c *NativeCore) portOwnerLocked(key portKey) string {
	return c.ports[key]
}

// releasePortLocked 释放 tag 名下的 key（不是它的就不动）；调用方持有 c.mu。
func (c *NativeCore) releasePortLocked(tag string, key portKey) {
	if c.ports[key] == tag {
		delete(c.ports, key)
	}
}

// PortOwner 返回占着 (port, l4) 的入站 tag，没有登记返回空串。诊断与测试用。
func (c *NativeCore) PortOwner(port int, l4 string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.ports[portKey{port: port, l4: strings.ToLower(l4)}]
}

// ============================================================
//  退场入站的流量
// ============================================================

// stashRetiredTraffic 把一个已关掉的入站的流量记到 tag 名下，下一次 GetTraffic
// 一并交出。入站换代（改配置）与删除时，旧适配器上还没取走的增量原先随它一起
// 丢掉；Close 会等在途连接收尾（适配器 wg.Wait），此时取到的就是最终值。
func (c *NativeCore) stashRetiredTraffic(tag string, in *nativeInbound) {
	if in == nil || in.adapter == nil {
		return
	}
	traffic, err := in.adapter.SnapshotTraffic()
	if err != nil || len(traffic) == 0 {
		return
	}
	c.mu.Lock()
	c.retiredTraffic[tag] = append(c.retiredTraffic[tag], traffic...)
	c.mu.Unlock()
}

// takeRetiredTrafficLocked 取出并清空 tag 名下的退场流量；调用方持有 c.mu。
func (c *NativeCore) takeRetiredTrafficLocked(tag string) []core.UserTraffic {
	out := c.retiredTraffic[tag]
	delete(c.retiredTraffic, tag)
	return out
}

// CloseAndDrainTraffic 关停内核并交出每个入站最后一轮流量。
//
// 进程退出时必须先关入站：TCP 连接要到连接结束才把流量入账，先上报再关会把
// 在途连接的流量全部丢掉（每次重启、升级都丢）。适配器 Close 会等连接
// goroutine 收尾，之后取到的就是最终值。重复调用返回空表。
func (c *NativeCore) CloseAndDrainTraffic() (map[string][]core.UserTraffic, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return map[string][]core.UserTraffic{}, nil
	}
	c.closed = true
	inbounds := make(map[string]*nativeInbound, len(c.inbounds))
	for tag, in := range c.inbounds {
		inbounds[tag] = in
		delete(c.inbounds, tag)
	}
	c.ports = make(map[portKey]string)
	c.mu.Unlock()

	var first error
	for tag, in := range inbounds {
		if err := closeNativeInbound(in); err != nil && first == nil {
			first = err
		}
		c.stashRetiredTraffic(tag, in)
	}
	c.connErrors.Close()

	c.mu.Lock()
	out := c.retiredTraffic
	c.retiredTraffic = make(map[string][]core.UserTraffic)
	c.mu.Unlock()
	return out, first
}
