package node

import (
	"log/slog"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"

	"github.com/aegispanel/nodeagent/core"
)

// Node 是一台服务器上的一个节点（入站）。只在 Server 的主循环里读写，不加锁。
type Node struct {
	id string
	// tag 是内核里的入站标识，就用节点 id：面板换协议时入站原地重建，tag 不变。
	tag string
	// nodeType 是当前协议（面板 NodeConfig.node_type），日志与入站配置都用它。
	nodeType string
	kernel   core.Core
	log      *slog.Logger
	// scope 是 Server.scope，登记入站归属用（registerInboundOwner）。
	scope string

	// known 是已下发给内核的用户，按用户号（订阅的 node_uid）记，一个号只有一份凭据。
	// 面板每次给全量时靠它算增量：全量重推会打断所有在线连接。
	known map[int64]core.User
	// userVersion 是内核里已是哪一版名单（u1-<hex>）；空串表示不确定，增量一律不收、要全量。
	// known 与 userVersion 说的是同一件事「内核里已是这一版」，只能经 resetUserMirror 一起作废。
	userVersion string
	// staged 是入站还没起来时收到的全量名单，装入站时一起交给内核（先有名单、再开 accept）。
	staged *stagedUsers
	// usersDirty 表示名单有了还没落盘的变化。
	usersDirty bool
	// pendingUsersAck 是节点操作中产生、还没交给会话的名单回执（随入站装上的 staged 名单的
	// ok=true，或用户表被清空时的 ok=false/resync）。Server 在每次节点操作之后经
	// flushUsersAck 发出并清空；没连着就直接丢掉（重连的 Hello 会报真实版本）。
	pendingUsersAck *nodev1.UsersAck

	// started 表示入站已建立、在服务。
	started bool
	// activeConfig 是正在服务的配置文档（解码后），回滚时装回它。
	activeConfig map[string]any
	// appliedSHA 是正在服务的配置的 content_sha256（Hello、回执都按它报）。
	appliedSHA string
	// appliedAt 是切到当前配置的时刻，health_passed 要等稳定窗口。
	appliedAt time.Time
	// healthPending 表示 switched 已报、health_passed 还没送到面板。
	healthPending bool
	// failed 是最近一个装不上的配置版本（坏版本台账），见 server_config.go。
	failed *applyFailure
	// lastApplyErr 是最近一次配置应用失败的原因（成功即清空），心跳的 reason 用它。
	lastApplyErr error
	// fromCache 表示正靠落盘缓存的配置服务、面板还没确认过（收到 Welcome 即清）。
	fromCache bool
	// offlineStart 只在冷启动走落盘缓存装入站的那一刻为真：装入站时用缓存的名单。
	offlineStart bool

	// acked 记本条流上已报过的配置回执（content_sha256 + 阶段），每条新流复位，见 server_config.go。
	acked map[string]bool

	// cache 是这个节点的落盘缓存（nil 即不落盘），见 cache.go。
	cache *nodeCache
}

// stagedUsers 是入站起来之前收到的一版全量名单。
type stagedUsers struct {
	version string
	users   []core.User
}

// newNode 建一个还没有入站的节点。
func (s *Server) newNode(id, nodeType string) *Node {
	n := &Node{
		id:       id,
		tag:      id,
		nodeType: nodeType,
		kernel:   s.kernel,
		log:      s.log.With("node", id),
		scope:    s.scope,
		known:    make(map[int64]core.User),
		acked:    make(map[string]bool),
	}
	if s.cache != nil {
		n.cache = s.cache.node(id)
	}
	return n
}

// resetAcks 在新流建立时调用：回执每条流各报一次，丢在断开的流上的由 Hello 对齐补回。
func (n *Node) resetAcks() { clear(n.acked) }
