// [INPUT]: 依赖 vless.go 的 vlessAdapter，依赖 core 的 User / UserTraffic
// [OUTPUT]: 对外提供 vlessAdapter 的 AddUsers、UpsertUsers、DelUsers、SnapshotTraffic、OnlineIPs；包内 snapshotUUIDs、lookupUser、enterDevice、leaveDevice、addTraffic
// [POS]: kernel 的 VLESS 用户表与计量：从 vless.go 拆出。热更新用户表，设备数按 IP 进出计数，流量按用户累加后由快照取走
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
)

// snapshotUUIDs 取当前已授权用户的原始 UUID，供 Vision 认帧用。
//
// 每条连接取一次快照而不是持有共享切片：用户增删随时可能发生，
// 拿着会变的底层数组去做逐字节比对，出问题的时候极难复现。
func (a *vlessAdapter) snapshotUUIDs() [][]byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([][]byte, 0, len(a.users))
	for id := range a.users {
		parsed, err := uuid.Parse(id)
		if err != nil {
			continue
		}
		buf := parsed
		out = append(out, append([]byte(nil), buf[:]...))
	}
	return out
}

func (a *vlessAdapter) lookupUser(id string) (core.User, bool) {
	a.mu.RLock()
	user, ok := a.users[id]
	a.mu.RUnlock()
	return user, ok
}

func (a *vlessAdapter) AddUsers(users []core.User) error {
	validated := make([]core.User, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.Parse(user.UUID)
		if err != nil {
			return fmt.Errorf("vless 用户 %q uuid 无效", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, user)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vless 适配器已关闭")
	}
	for _, user := range validated {
		if _, exists := a.users[user.UUID]; !exists {
			a.users[user.UUID] = user
		}
	}
	return nil
}

func (a *vlessAdapter) UpsertUsers(users []core.User) error {
	validated := make([]core.User, 0, len(users))
	for _, user := range users {
		parsed, err := uuid.Parse(user.UUID)
		if err != nil {
			return fmt.Errorf("vless 用户 %q uuid 无效", user.UUID)
		}
		user.UUID = parsed.String()
		validated = append(validated, user)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vless 适配器已关闭")
	}
	for _, user := range validated {
		if previous, exists := a.users[user.UUID]; exists {
			a.limiters.Remove(previous.ID)
		}
		a.users[user.UUID] = user
	}
	return nil
}

func (a *vlessAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			key := parsed.String()
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			if user, ok := a.users[key]; ok {
				a.limiters.Remove(user.ID)
			}
			delete(a.users, key)
		}
	}
	return nil
}

func (a *vlessAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, traffic := range a.traffic {
		if traffic.Upload != 0 || traffic.Download != 0 {
			out = append(out, core.UserTraffic{ID: id, Upload: traffic.Upload, Download: traffic.Download})
		}
		delete(a.traffic, id)
	}
	return out, nil
}

func (a *vlessAdapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, ips := range a.online {
		for ip := range ips {
			out[id] = append(out[id], ip)
		}
	}
	return out
}

func (a *vlessAdapter) enterDevice(user core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[user.ID]
	if set == nil {
		set = make(map[string]struct{})
		a.online[user.ID] = set
	}
	if _, exists := set[ip]; !exists && user.DeviceLimit > 0 && len(set) >= user.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}

func (a *vlessAdapter) leaveDevice(user core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[user.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, user.ID)
		}
	}
}

func (a *vlessAdapter) addTraffic(user core.User, upload, download int64) {
	a.mu.Lock()
	a.traffic[user.ID] = core.UserTraffic{ID: user.ID, Upload: a.traffic[user.ID].Upload + upload, Download: a.traffic[user.ID].Download + download}
	a.mu.Unlock()
}
