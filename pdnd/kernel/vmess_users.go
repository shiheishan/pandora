// [INPUT]: 依赖 vmess.go 的 vmessAdapter 与 vmessUser，依赖 core 的 User / UserTraffic
// [OUTPUT]: 对外提供 vmessAdapter 的 AddUsers、UpsertUsers、DelUsers、SnapshotTraffic、OnlineIPs；包内 enterDevice、leaveDevice、addTraffic
// [POS]: kernel 的 VMess 用户表与计量：从 vmess.go 拆出。热更新用户时重算命令密钥，设备数按 IP 进出计数，流量按用户累加后由快照取走
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
)

func (a *vmessAdapter) AddUsers(users []core.User) error {
	validated := make([]vmessUserEntry, 0, len(users))
	for _, u := range users {
		parsed, err := uuid.Parse(u.UUID)
		if err != nil {
			return fmt.Errorf("vmess user %q uuid invalid", u.UUID)
		}
		validated = append(validated, vmessUserEntry{uuid: parsed.String(), user: u, key: vmessCommandKey(parsed)})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vmess adapter closed")
	}
	for _, entry := range validated {
		if _, ok := a.users[entry.uuid]; !ok {
			a.users[entry.uuid] = vmessUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, key: entry.key}
		}
	}
	return nil
}

type vmessUserEntry struct {
	uuid string
	user core.User
	key  [16]byte
}

func (a *vmessAdapter) UpsertUsers(users []core.User) error {
	validated := make([]vmessUserEntry, 0, len(users))
	for _, u := range users {
		parsed, err := uuid.Parse(u.UUID)
		if err != nil {
			return fmt.Errorf("vmess user %q uuid invalid", u.UUID)
		}
		u.UUID = parsed.String()
		validated = append(validated, vmessUserEntry{uuid: u.UUID, user: u, key: vmessCommandKey(parsed)})
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vmess adapter closed")
	}
	for _, entry := range validated {
		if previous, exists := a.users[entry.uuid]; exists {
			a.limiters.Remove(previous.ID)
		}
		a.users[entry.uuid] = vmessUser{ID: entry.user.ID, DeviceLimit: entry.user.DeviceLimit, SpeedLimit: entry.user.SpeedLimit, key: entry.key}
	}
	return nil
}

func (a *vmessAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			key := parsed.String()
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			if entry, ok := a.users[key]; ok {
				a.limiters.Remove(entry.ID)
			}
			delete(a.users, key)
		}
	}
	return nil
}

func (a *vmessAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]core.UserTraffic, 0, len(a.traffic))
	for id, t := range a.traffic {
		if t.Upload != 0 || t.Download != 0 {
			out = append(out, t)
		}
		delete(a.traffic, id)
	}
	return out, nil
}

func (a *vmessAdapter) OnlineIPs() map[int64][]string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make(map[int64][]string, len(a.online))
	for id, set := range a.online {
		for ip := range set {
			out[id] = append(out[id], ip)
		}
	}
	return out
}

func (a *vmessAdapter) enterDevice(u core.User, ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	set := a.online[u.ID]
	if set == nil {
		set = map[string]struct{}{}
		a.online[u.ID] = set
	}
	if _, ok := set[ip]; !ok && u.DeviceLimit > 0 && len(set) >= u.DeviceLimit {
		return false
	}
	set[ip] = struct{}{}
	return true
}

func (a *vmessAdapter) leaveDevice(u core.User, ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if set := a.online[u.ID]; set != nil {
		delete(set, ip)
		if len(set) == 0 {
			delete(a.online, u.ID)
		}
	}
}

func (a *vmessAdapter) addTraffic(u core.User, up, down int64) {
	a.mu.Lock()
	t := a.traffic[u.ID]
	t.ID = u.ID
	t.Upload += up
	t.Download += down
	a.traffic[u.ID] = t
	a.mu.Unlock()
}
