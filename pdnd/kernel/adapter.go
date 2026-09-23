package kernel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strings"
	"sync"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/route"
	M "github.com/sagernet/sing/common/metadata"
)

// InboundSpec is the stable input given to a Pandora-native protocol adapter.
// Adapters must not read panel or database objects directly.
type InboundSpec struct {
	Config     core.InboundConfig
	Generation uint64
}

// DataPlane is the only transport surface an inbound adapter may use for
// routed traffic. It keeps protocol decoding separate from routing and egress.
type DataPlane interface {
	DialTCP(context.Context, route.Meta, M.Socksaddr) (net.Conn, error)
	ListenUDP(context.Context, route.Meta, M.Socksaddr) (net.PacketConn, error)
}

type AdapterHooks struct {
	DataPlane DataPlane

	// OnConnError 报告单条连接为什么没能服务成功。可以为 nil。
	//
	// 在此之前所有失败——TLS 握手不过、REALITY 校验不通过、用户未授权、
	// 协议解析出错——统统是 conn.Close() 走人。客户端那头只看得到 EOF，
	// 服务端这头什么都不留。排查一个「连不上」的节点时，运维手上没有
	// 任何信息，只能靠猜。
	//
	// 一条连接失败不是服务级故障，所以这里是观测出口而不是错误返回：
	// 实现方自己决定按什么级别记、采样多少、要不要限流。
	OnConnError func(ConnError)
}

// ConnError 是一条连接的失败现场。
type ConnError struct {
	// Tag 是出问题的入站标签。
	Tag string
	// Protocol 如 vless / vmess。
	Protocol string
	// Stage 失败发生在哪一步，取值见下面的 Stage 常量。
	// 这是最有用的一维：同样是「连不上」，卡在 TLS 握手和卡在用户
	// 鉴权，要查的方向完全不同。
	Stage string
	// Remote 对端地址，可能为 nil。
	Remote net.Addr
	Err    error
}

// 连接失败的阶段。
const (
	// StageTLSHandshake：TLS/REALITY 握手没过。客户端配置、证书、
	// SNI、short-id 对不上都落在这里。
	StageTLSHandshake = "tls-handshake"
	// StageRealityInspect：连上了但拿不到 REALITY 会话信息。
	StageRealityInspect = "reality-inspect"
	// StageSession：握手已过，协议层出错——用户未授权、请求头非法、
	// 流控不支持、目标连不上。
	StageSession = "session"
)

// Adapter is the native protocol boundary. REALITY/XHTTP adapters implement
// this interface directly; they do not embed a complete third-party kernel or
// call panel APIs.
type Adapter interface {
	Protocol() string
	Validate(InboundSpec) error
	Start(context.Context, InboundSpec, AdapterHooks) error
	Close() error
	AddUsers([]core.User) error
	UpsertUsers([]core.User) error
	DelUsers([]string) error
	SnapshotTraffic() ([]core.UserTraffic, error)
	OnlineIPs() map[int64][]string
}

type AdapterFactory func(InboundSpec) (Adapter, error)

type AdapterRegistry struct {
	mu        sync.RWMutex
	factories map[string]AdapterFactory
}

func NewAdapterRegistry() *AdapterRegistry {
	return &AdapterRegistry{factories: make(map[string]AdapterFactory)}
}

func (r *AdapterRegistry) Register(protocol string, factory AdapterFactory) error {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" || factory == nil {
		return fmt.Errorf("协议和适配器工厂不能为空")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[protocol]; exists {
		return fmt.Errorf("协议 %q 已注册", protocol)
	}
	r.factories[protocol] = factory
	return nil
}

func (r *AdapterRegistry) New(spec InboundSpec) (Adapter, error) {
	protocol := strings.ToLower(strings.TrimSpace(spec.Config.Protocol))
	r.mu.RLock()
	factory, ok := r.factories[protocol]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("原生内核不支持协议 %q", protocol)
	}
	adapter, err := factory(spec)
	if err != nil {
		return nil, fmt.Errorf("创建 %s 原生适配器: %w", protocol, err)
	}
	if adapter == nil || strings.ToLower(strings.TrimSpace(adapter.Protocol())) != protocol {
		return nil, fmt.Errorf("协议 %q 适配器返回了不一致的协议标识", protocol)
	}
	if err := adapter.Validate(spec); err != nil {
		return nil, fmt.Errorf("校验 %s 原生适配器: %w", protocol, err)
	}
	return adapter, nil
}

func (r *AdapterRegistry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.factories))
	for protocol := range r.factories {
		types = append(types, protocol)
	}
	sort.Strings(types)
	return types
}

// reportAdapterConnError 把一条连接的失败原因交给 hook。
//
// 抽成包级函数是因为每个协议都需要它，而各协议自己实现的话，「哪些错误
// 不该上报」这条规则就会各写一遍、各漏一处。Trojan 一度把 handleConn 的
// 返回值直接 `_ =` 掉，结果互操作失败时服务端一行日志都没有，只能靠猜。
//
// io.EOF 和 net.ErrClosed 每条连接正常结束时都会出现，报上去只会把真正
// 的错误淹掉。
func reportAdapterConnError(hook func(ConnError), tag, protocol, stage string, conn net.Conn, err error) {
	if hook == nil || err == nil {
		return
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return
	}
	var remote net.Addr
	if conn != nil {
		remote = conn.RemoteAddr()
	}
	hook(ConnError{Tag: tag, Protocol: protocol, Stage: stage, Remote: remote, Err: err})
}
