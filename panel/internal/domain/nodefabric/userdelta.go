package nodefabric

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
)

// 用户列表的增量下发。
//
// 节点端原先每轮都拉全量：16 个用户无所谓，几千个用户时每 15 秒一次全量
// 传输加全量解析，面板和节点两头都在做无用功，而绝大多数轮次里列表根本
// 没变。
//
// 分两条路，各管各的：
//
//   - REST 轮询只做 ETag / 304。列表没变就回一个空响应，绝大多数轮次都
//     落在这里，省掉的是传输和解析两头的开销。
//   - 真正的增量走 WebSocket。
//
// 为什么增量不能在 REST 上做：差异要知道"节点端手上是哪一份"才算得出来，
// 而 ETag 是哈希、不可逆，从它复原不出旧列表。REST 下唯一的办法是服务端
// 缓存历史版本，但面板是可以多副本部署的，缓存不共享，节点这次打到 A
// 副本、下次打到 B，命中率没有保证——省下的传输还不够补上缓存不命中时
// 的全量重传。
//
// WebSocket 就没这个问题：长连接本身是有状态的，服务端清楚这条连接推过
// 哪一版，差异算得准。这也是上游 Xboard-Node 把 sync.user.delta 放在 WS
// 上而不是 REST 上的原因。
//
// 所以这个文件里的 DiffUsers 是给 WS 那条路用的；REST 只用到
// UserSetVersion。

// UserSetVersion 是一份用户列表的指纹。
//
// 只覆盖会影响节点行为的字段：ID、UUID、限速、设备数。别的字段变了不该
// 触发一次下发。
func UserSetVersion(users []ProxyUser) string {
	sorted := make([]ProxyUser, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })

	h := sha256.New()
	for _, u := range sorted {
		// 分隔符不能省：没有它的话 (id=1,uuid="23") 和 (id=12,uuid="3")
		// 会算出同一个摘要。
		h.Write([]byte(strconv.FormatInt(u.ID, 10)))
		h.Write([]byte{0})
		h.Write([]byte(u.UUID))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(u.SpeedLimit)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.Itoa(u.DeviceLimit)))
		h.Write([]byte{0, 0})
	}
	return `"u1-` + hex.EncodeToString(h.Sum(nil)[:16]) + `"`
}

// UserDelta 是两份用户列表之间的差异。
type UserDelta struct {
	// Added 既包含新增的用户，也包含限速或设备数被改过的——对节点端来说
	// 两者处理方式一样：按这份数据覆盖本地记录。分成两类只会让节点端多
	// 一条分支，而那条分支做的事和 Added 完全相同。
	Added []ProxyUser `json:"added"`
	// Removed 只给 ID：节点端拿它删本地记录，不需要别的字段。
	Removed []int64 `json:"removed"`
}

// Empty 表示两份列表一致。
func (d UserDelta) Empty() bool { return len(d.Added) == 0 && len(d.Removed) == 0 }

// DiffUsers 算出从 old 到 now 的差异。
func DiffUsers(old, now []ProxyUser) UserDelta {
	prev := make(map[int64]ProxyUser, len(old))
	for _, u := range old {
		prev[u.ID] = u
	}

	var delta UserDelta
	for _, u := range now {
		before, existed := prev[u.ID]
		if !existed || before != u {
			delta.Added = append(delta.Added, u)
		}
		delete(prev, u.ID)
	}
	// 留在 prev 里的是这一轮不该再有的
	for id := range prev {
		delta.Removed = append(delta.Removed, id)
	}
	// 排序让输出稳定：同样的输入产生同样的字节，便于比对和测试
	sort.Slice(delta.Added, func(i, j int) bool { return delta.Added[i].ID < delta.Added[j].ID })
	sort.Slice(delta.Removed, func(i, j int) bool { return delta.Removed[i] < delta.Removed[j] })
	return delta
}
