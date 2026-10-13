package panel

import (
	"errors"
	"fmt"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
)

// maxUsersChunks 是一版全量名单的块数上限（每块 5000 人，约 3 亿人），挡住异常的 chunk_total。
const maxUsersChunks = 1 << 16

// usersAssembler 把 UsersFull 的分块按节点收齐：chunk_index 从 0 起逐块递增，
// 收齐 chunk_total 块才交出整份。只在会话的流 goroutine 里用，不加锁；每条新流一个。
type usersAssembler struct {
	parts map[string]*usersPartial
}

type usersPartial struct {
	version     string
	total, next uint32
	users       []*nodev1.User
}

func newUsersAssembler() *usersAssembler {
	return &usersAssembler{parts: make(map[string]*usersPartial)}
}

// add 收一块。收齐返回整份；还没收齐返回 (nil, nil)；块对不上返回错误并丢掉这个节点手上的半份。
// chunk_total 为 0 按 1 算（空名单也是一块）。
func (a *usersAssembler) add(m *nodev1.UsersFull) (*UsersFull, error) {
	node := m.GetNodeId()
	if node == "" {
		return nil, errors.New("名单块缺少 node_id")
	}
	total := max(m.GetChunkTotal(), 1)
	if total > maxUsersChunks {
		delete(a.parts, node)
		return nil, fmt.Errorf("名单块数 %d 超过上限", total)
	}
	p := a.parts[node]
	if m.GetChunkIndex() == 0 {
		p = &usersPartial{version: m.GetVersion(), total: total}
		a.parts[node] = p
	} else if p == nil || p.version != m.GetVersion() || p.total != total || p.next != m.GetChunkIndex() {
		delete(a.parts, node)
		return nil, fmt.Errorf("名单块不连续：收到第 %d/%d 块", m.GetChunkIndex(), total)
	}
	p.users = append(p.users, m.GetUsers()...)
	p.next++
	if p.next < p.total {
		return nil, nil
	}
	delete(a.parts, node)
	return &UsersFull{NodeID: node, Version: p.version, Users: p.users}, nil
}
