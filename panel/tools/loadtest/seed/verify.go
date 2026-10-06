package seed

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

type verifyReport struct {
	NodesChecked int
	UsersPerNode int
	SubscribeOK  bool
}

// verifySeed 核对首尾两个节点（用户列表一次可能上 MB，全部节点都拉一遍就成了压测本身）。
func verifySeed(ctx context.Context, nc *nodeClient, nodes []*seededNode, users []ltkit.ManifestUser,
	publicBase, subPrefix string) (*verifyReport, error) {
	rep := &verifyReport{}
	picks := []*seededNode{nodes[0]}
	if len(nodes) > 1 {
		picks = append(picks, nodes[len(nodes)-1])
	}
	want := make(map[int64]bool, len(users))
	for _, u := range users {
		want[u.NodeUID] = true
	}
	for _, n := range picks {
		cfg, err := nc.signedGet(ctx, n.Identity, n.ID, "/v1/nodes/effective-config")
		if err != nil {
			return nil, fmt.Errorf("node %s signed effective-config: %w", n.Name, err)
		}
		if len(cfg) == 0 {
			return nil, fmt.Errorf("node %s effective-config is empty", n.Name)
		}
		if _, err := nc.uniProxyGet(ctx, n.Identity.RuntimeToken, n.ID, seedNodeType, "config"); err != nil {
			return nil, fmt.Errorf("node %s UniProxy config: %w", n.Name, err)
		}
		list, err := nc.uniProxyGet(ctx, n.Identity.RuntimeToken, n.ID, seedNodeType, "user")
		if err != nil {
			return nil, fmt.Errorf("node %s UniProxy user: %w", n.Name, err)
		}
		got, err := uniProxyUserIDs(list)
		if err != nil {
			return nil, fmt.Errorf("node %s UniProxy user: %w", n.Name, err)
		}
		if err := checkUserSet(got, want); err != nil {
			return nil, fmt.Errorf("node %s UniProxy user list: %w", n.Name, err)
		}
		rep.NodesChecked++
		rep.UsersPerNode = len(got)
	}
	if publicBase != "" && subPrefix != "" && len(users) > 0 {
		if err := pullOnce(ctx, publicBase+"/"+subPrefix+"/"+users[0].SubscribeToken, users[0].RealIP); err != nil {
			return nil, err
		}
		rep.SubscribeOK = true
	}
	return rep, nil
}

// uniProxyUserIDs 取 users[].id；响应可能直接是 {users: [...]}，也可能包一层 data（与冒烟 seed.ts 同样两种都认）。
func uniProxyUserIDs(resp jsonObject) ([]int64, error) {
	raw := lookup(resp, "users")
	if raw == nil {
		raw = lookup(resp, "data.users")
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("response has no users array: %.300s", mustJSON(resp))
	}
	ids := make([]int64, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("user entry is not an object: %.200s", mustJSON(item))
		}
		id, err := num(m, "id")
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// checkUserSet 要求节点拿到的用户恰好是这一批造出的全部用户：少了说明套餐 / 池 / 版本绑定没接上，
// 多了说明别的套餐也授权了这个池（压测的用户规模就不准了）。
func checkUserSet(got []int64, want map[int64]bool) error {
	seen := make(map[int64]bool, len(got))
	for _, id := range got {
		if !want[id] {
			return fmt.Errorf("unexpected user id %d (not seeded by this run)", id)
		}
		if seen[id] {
			return fmt.Errorf("duplicate user id %d", id)
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		return fmt.Errorf("got %d users, want all %d seeded users", len(seen), len(want))
	}
	return nil
}

// pullOnce 以第一个用户的身份真拉一次订阅（占他每小时 60 次额度里的 1 次）。
// 节点还没心跳过，订阅正文里的节点是空的，这里只确认凭据、前缀与订阅状态这条链是通的。
func pullOnce(ctx context.Context, url, realIP string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "pandora-loadtest-seed")
	req.Header.Set("X-Real-IP", realIP)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("subscription pull: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Subscription-Userinfo") == "" {
		return fmt.Errorf("subscription pull: HTTP %d (want 200 with Subscription-Userinfo)", resp.StatusCode)
	}
	return nil
}
