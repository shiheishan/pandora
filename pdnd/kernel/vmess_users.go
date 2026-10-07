package kernel

import (
	"crypto/cipher"
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
		entry, err := newVMessUserEntry(parsed.String(), u, parsed)
		if err != nil {
			return err
		}
		validated = append(validated, entry)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return fmt.Errorf("vmess adapter closed")
	}
	for _, entry := range validated {
		if _, ok := a.users[entry.uuid]; !ok {
			a.users[entry.uuid] = entry.stored()
		}
	}
	a.publishAuthCandidatesLocked()
	return nil
}

type vmessUserEntry struct {
	uuid      string
	user      core.User
	key       [16]byte
	authBlock cipher.Block
}

func (a *vmessAdapter) UpsertUsers(users []core.User) error {
	validated := make([]vmessUserEntry, 0, len(users))
	for _, u := range users {
		parsed, err := uuid.Parse(u.UUID)
		if err != nil {
			return fmt.Errorf("vmess user %q uuid invalid", u.UUID)
		}
		u.UUID = parsed.String()
		entry, err := newVMessUserEntry(u.UUID, u, parsed)
		if err != nil {
			return err
		}
		validated = append(validated, entry)
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
		a.users[entry.uuid] = entry.stored()
	}
	a.publishAuthCandidatesLocked()
	return nil
}

func (a *vmessAdapter) DelUsers(ids []string) error {
	a.mu.Lock()
	var removed []int64
	for _, id := range ids {
		if parsed, err := uuid.Parse(id); err == nil {
			key := parsed.String()
			// 顺手丢掉这个用户的令牌桶，否则用户删了桶还留着。
			if entry, ok := a.users[key]; ok {
				a.limiters.Remove(entry.ID)
				removed = append(removed, entry.ID)
			}
			delete(a.users, key)
		}
	}
	a.publishAuthCandidatesLocked()
	a.mu.Unlock()
	// 先删表、再踢线（锁外关）：已有的长连接、mux / QUIC 会话随之断开。
	a.sessions.revoke(removed)
	return nil
}

func (a *vmessAdapter) SnapshotTraffic() ([]core.UserTraffic, error) {
	return a.sessions.snapshot(), nil
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
	a.sessions.add(u.ID, up, down)
}
