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

// PortInUseError 是「端口已被占用」。
//
// 错误文案会经 failed 回执与 degraded 心跳交给面板，所以不能泄露别的面板的
// 东西：占用者与申请者属于同一个面板（同一 panel URL 加同一租户，见
// SetInboundOwner）时才写出对方的节点 ID（OwnerNodeID）；属于别的面板、身份
// 不明、或是本机其他进程（bind 返回 EADDRINUSE），一律只说「本机其他服务」。
type PortInUseError struct {
	Port int
	L4   string // "tcp" / "udp"
	// OwnerNodeID 只在同一面板时填。
	OwnerNodeID string
	// owner 是占用者的入站 tag，只留给本进程诊断与测试，不进任何文案。
	owner string
	Err   error // 外部占用时 bind 的原始错误
}

func (e *PortInUseError) Error() string {
	key := portKey{port: e.Port, l4: e.L4}.String()
	if e.OwnerNodeID == "" {
		return "端口 " + key + " 已被本机其他服务占用"
	}
	return "端口 " + key + " 已被节点 " + e.OwnerNodeID + " 占用"
}

func (e *PortInUseError) Unwrap() error { return e.Err }

// RuntimeReason 是给面板的机器可读原因（纯 ASCII，可放进请求头）：
//
//	port_in_use:<端口>/<tcp|udp>:<节点 ID>   占用者是同一面板的另一个节点
//	port_in_use:<端口>/<tcp|udp>:other       其余情况（别的面板、其他进程）
//
// 节点端经 errors.As 取这个方法（不 import kernel），随 degraded 心跳上报。
func (e *PortInUseError) RuntimeReason() string {
	owner := e.OwnerNodeID
	if owner == "" {
		owner = "other"
	}
	return fmt.Sprintf("port_in_use:%d/%s:%s", e.Port, e.L4, owner)
}

// inboundOwner 是一个入站属于哪个面板、哪个节点。scope 是面板标识（节点端
// 给的不透明串：panel URL 与租户的哈希），为空表示不明。
type inboundOwner struct {
	scope  string
	nodeID string
}

// SetInboundOwner 登记 tag 属于哪个面板（scope）的哪个节点。节点端在每次应用
// 配置前调用；登记只影响端口冲突时的文案，不影响谁占端口。scope 为空表示
// 不明（例如兼容通道拿不到租户），这样的入站之间互不透露节点 ID。
func (c *NativeCore) SetInboundOwner(tag, scope, nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.owners[tag] = inboundOwner{scope: scope, nodeID: nodeID}
}

// portConflictLocked 构造「key 被 ownerTag 占着」的错误，按同面板与否决定能
// 不能写出对方节点 ID；调用方持有 c.mu。
func (c *NativeCore) portConflictLocked(key portKey, requesterTag, ownerTag string) *PortInUseError {
	err := &PortInUseError{Port: key.port, L4: key.l4, owner: ownerTag}
	requester, owner := c.owners[requesterTag], c.owners[ownerTag]
	if requester.scope != "" && requester.scope == owner.scope && owner.nodeID != "" {
		err.OwnerNodeID = owner.nodeID
	}
	return err
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
