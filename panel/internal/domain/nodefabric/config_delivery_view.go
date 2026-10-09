package nodefabric

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// 节点配置视图（w10quiet）：节点每 15 秒的「配置变了没」不再进 PG。
//
// 静默时每个节点每轮拉取都要问三件事：UniProxy 令牌还对不对（拉名单、上报前的认证）、
// 手上的生效发布还是不是当前版（effective-config 的 204）、心跳回包里的期望版本。三者
// 只取决于节点行的非遥测列、所属服务器的状态与生效发布物——这些一变，迁移 00153 的
// 触发器就在提交后发 'c' 通知。所以纪元监听健康时，按节点缓存一份视图，加载前记下
// 监听戳，之后只要戳还覆盖请求时的戳，视图就是准的，回答直接从内存出。
//
// 只在监听健康时用；不健康时调用方照旧查库（AuthenticateNode、EffectiveConfigUnchangedAt、
// 心跳写）。视图里的门槛与那几条查询用同一份 SQL 片段（uniProxyServingGateSQL、
// effectiveDeliverableSQL），不另写一份口径。令牌对不上、门槛不过、节点不存在都不在
// 这里下结论：交回查库路径，错误码与日志和原来逐字相同。

// nodeConfigCacheTTL 只是上限：视图在监听戳覆盖时不看 TTL（pinned），过期只为回收内存。
const nodeConfigCacheTTL = 10 * time.Minute

// effectiveDeliverableSQL 是「节点可以拿生效发布」的门槛（别名 n）：拉生效配置的全量路径、
// 204 判定与配置视图共用。
const effectiveDeliverableSQL = `n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'
				   AND n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535`

// uniProxyServingGateSQL 是 UniProxy 认证的节点门槛（别名 n 节点、s 服务器，不含令牌）：
// AuthenticateNode 的 WHERE 与配置视图共用。
func uniProxyServingGateSQL() string {
	return `s.deleted_at IS NULL
			   AND s.status IN ('ready','draining')
			   AND n.serving_status IN ('active','draining')
			   AND n.node_type IS NOT NULL
			   AND n.server_port BETWEEN 1 AND 65535
			   AND ` + StableProtocolReadySQL("n")
}

// nodeConfigView 是一个节点配置与认证输入在某一刻的快照。共享、只读。
type nodeConfigView struct {
	watch watchStamp
	// UniProxy 认证
	serving   ServingNode
	tokenHash []byte
	uniOK     bool
	isControl bool
	// 生效发布
	effOK             bool
	cfgGeneration     int64
	desiredReleaseID  *string
	desiredGeneration *int64
	releaseID, keyID  *string
	// 心跳回包
	status               string
	desiredConfigVersion int
}

var errNodeConfigViewMissing = errors.New("node config view: node not found")

// effectiveUnchanged 与 EffectiveConfigUnchangedAt 的 EXISTS 同一组条件。
func (v *nodeConfigView) effectiveUnchanged(releaseID string, generation uint64, keyID string) bool {
	g := int64(generation)
	return v.effOK && v.cfgGeneration == g &&
		v.desiredReleaseID != nil && *v.desiredReleaseID == releaseID &&
		v.desiredGeneration != nil && *v.desiredGeneration == g &&
		v.releaseID != nil && *v.releaseID == releaseID &&
		v.keyID != nil && *v.keyID == keyID
}

// tokenMatches 比较 UniProxy 令牌的哈希（定长比较）。
func (v *nodeConfigView) tokenMatches(token string) bool {
	return token != "" && len(v.tokenHash) > 0 &&
		subtle.ConstantTimeCompare(v.tokenHash, crypto.HashToken(token)) == 1
}

// cachedNodeConfig 取节点配置视图。want 不健康、节点 ID 不规范、视图加载时监听恰好不健康，
// 或者节点不存在，都返回 ok=false：调用方走原来的查库路径。
func (s *Service) cachedNodeConfig(ctx context.Context, tenantID, nodeID string, want watchStamp) (*nodeConfigView, bool) {
	if s.caches == nil || !want.ok() {
		return nil, false
	}
	parsed, err := uuid.Parse(nodeID)
	if err != nil {
		return nil, false
	}
	nodeID = parsed.String()
	covers := func(v *nodeConfigView) bool { return v.watch.configCovers(want) }
	v, err := s.caches.config.get(ctx, identityCacheKey(tenantID, nodeID), want.flight("c"), covers, covers,
		func(ctx context.Context) (*nodeConfigView, error) { return s.loadNodeConfigView(ctx, tenantID, nodeID) })
	if err != nil || !covers(v) {
		return nil, false
	}
	return v, true
}

// loadNodeConfigView 一次往返读出视图。戳在查询之前取：查询的快照晚于戳里每一条通知对应的提交。
func (s *Service) loadNodeConfigView(ctx context.Context, tenantID, nodeID string) (*nodeConfigView, error) {
	v := &nodeConfigView{watch: s.watchStamp()}
	n := &v.serving
	var proto []byte
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, `
			SELECT n.id::text, n.name, coalesce(n.node_type,''), coalesce(n.server_host,''),
			       coalesce(n.server_port,0), n.traffic_rate, n.protocol_config, n.pool_id,
			       n.status, coalesce(n.kernel,'auto'), coalesce(s.status,''), n.serving_status,
			       coalesce(s.control_node_id=n.id,false), n.server_token_hash,
			       coalesce(`+uniProxyServingGateSQL()+`, false),
			       coalesce(`+effectiveDeliverableSQL+`, false),
			       n.config_source_generation, n.desired_effective_release_id::text, n.desired_effective_generation,
			       r.id::text, r.key_id, coalesce(n.desired_config_version,0), `+deliveryEpochSQL+`
			  FROM nodes n
			  LEFT JOIN servers s ON s.tenant_id=n.tenant_id AND s.id=n.server_id
			  LEFT JOIN node_effective_config_releases r
			    ON r.tenant_id=n.tenant_id AND r.node_id=n.id AND r.generation=n.config_source_generation
			 WHERE n.tenant_id=$1 AND n.id=$2::uuid`,
		[]any{tenantID, nodeID},
		&n.ID, &n.Name, &n.NodeType, &n.ServerHost, &n.ServerPort, &n.TrafficRate, &proto, &n.PoolID,
		&n.Status, &n.Kernel, &n.ServerStatus, &n.ServingStatus, &v.isControl, &v.tokenHash,
		&v.uniOK, &v.effOK, &v.cfgGeneration, &v.desiredReleaseID, &v.desiredGeneration,
		&v.releaseID, &v.keyID, &v.desiredConfigVersion, &n.deliveryEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNodeConfigViewMissing
	}
	if err != nil {
		return nil, err
	}
	n.NodeType = CanonicalNodeType(n.NodeType)
	n.Protocol = json.RawMessage(proto)
	n.epochKnown = true
	v.status = n.Status
	return v, nil
}

// servingNode 按视图做 UniProxy 认证；ok=false 表示这里不下结论（令牌不对、门槛
// 不过、控制节点生命周期不允许），交回查库路径给出原来的错误。
func (v *nodeConfigView) servingNode(token, declaredType string, want watchStamp) (*ServingNode, bool) {
	if !v.uniOK || !v.tokenMatches(token) || !legacyNodeStatusAllowsServing(v.isControl, v.serving.Status) {
		return nil, false
	}
	n := v.serving // 拷贝：handler 会往上填出站与分流
	if requested := CanonicalNodeType(declaredType); requested != "" && requested != n.NodeType {
		n.DeclaredType = requested
	}
	n.watch = want
	return &n, true
}
