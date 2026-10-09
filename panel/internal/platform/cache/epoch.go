package cache

// 纪元序列与通知通道的约定。序列、触发器与通知都在迁移里（00101、00153–00156），这里只放名字，
// 供各域在本来就要跑的查询里读纪元（EpochSQL）、或起 Watch 监听通知。名字与迁移对不上时
// PG18 用例 TestCacheEpochPG18 变红。

// EpochSQL 返回读出纪元序列当前值的子查询，嵌进本来就要跑的查询里，不单独多一次往返。
//
// 要加上 is_called：新建的序列 last_value=1、is_called=false，第一次 nextval 返回 1、
// last_value 还是 1，只把 is_called 翻成 true。只读 last_value 会漏掉全库的第一次推进。
//
// sequence 只能是本文件里的常量（直接拼进 SQL）。
func EpochSQL(sequence string) string {
	return `(SELECT last_value + is_called::int FROM ` + sequence + `)`
}

// 节点下发纪元（00101）与它的通知通道（00153）。aegis-node 的名单、身份与配置视图缓存用它。
const (
	// NodeDeliveryEpoch：订阅、配额用尽翻转、流量包、套餐版本、池授权、用户组、账号状态与时区、
	// 系统设置、站点时区、节点身份与节点状态变化时推进。
	NodeDeliveryEpoch = "node_delivery_epoch"
	// NodeEpochChannel 的载荷：'d' 下发输入变了（随 NodeDeliveryEpoch 推进发出），'c' 节点自己的
	// 配置与认证输入变了（00153 的 app.notify_node_config_change）。
	NodeEpochChannel = "aegis_node_epoch"
)

// 缓存纪元（00155、00156）：都由 app.bump_cache_epoch('<种类>') 在提交时推进序列 <种类>_epoch，
// 并在 CacheEpochChannel 上发载荷 '<种类>'。
const (
	CacheEpochChannel = "aegis_cache_epoch"

	// KindNodeCatalog：订阅里能看到的节点集合与它们的连接参数——节点的非遥测列（与 00153 的 'c'
	// 同一份清单，含池归属）、已应用的发布物、节点增删、首次心跳与掉线后恢复心跳、服务器的
	// 状态 / 删除 / 控制节点、配置应用记录。套餐绑池、池的用户组限定在下发纪元里。
	KindNodeCatalog = "node_catalog"
	// KindCatalog：门户目录——套餐（库存计数除外）、套餐版本、价格、配额定义、流量包。
	KindCatalog = "catalog"
	// KindAppearance：外观——主题与插槽。
	KindAppearance = "appearance"
	// KindSiteSettings：站点设置——租户行（站点名、时区、订阅路径前缀等）、系统设置、降级开关。
	KindSiteSettings = "site_settings"
)

// CacheKinds 是 CacheEpochChannel 上的全部种类（每次返回新切片），下标即 NewWatch 的 kinds 下标。
func CacheKinds() []string {
	return []string{KindNodeCatalog, KindCatalog, KindAppearance, KindSiteSettings}
}

// EpochSequence 是种类对应的纪元序列名（<种类>_epoch）。
func EpochSequence(kind string) string { return kind + "_epoch" }
