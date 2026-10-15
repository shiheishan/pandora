// 拉取快照里的订阅域名分组与选取规则：按用户组找组，没分到用缺省组，都没有则回退面板对外地址（由调用方处理）。
// 组内按有效权重（基础权重 × Redis 分数）随机选取（crypto/rand）；
// Surfboard 托管配置取基础权重最高的域名，同权重取排在前面的。

package service

import (
	"context"
	"crypto/rand"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// domainSet 是 extras 快照里的一份只读域名分组索引。
type domainSet struct {
	groups      []DomainGroup
	byUserGroup map[uuid.UUID]int
	defaultIdx  int
	known       map[string]struct{}
}

// newDomainSet 从组列表构建索引；known 收录全部规范化基址。
func newDomainSet(groups []DomainGroup) domainSet {
	d := domainSet{
		groups:      groups,
		byUserGroup: make(map[uuid.UUID]int),
		defaultIdx:  -1,
		known:       make(map[string]struct{}),
	}
	for i, g := range groups {
		if g.IsDefault && d.defaultIdx < 0 {
			d.defaultIdx = i
		}
		for _, ug := range g.UserGroupIDs {
			d.byUserGroup[ug] = i
		}
		for _, dom := range g.Domains {
			d.known[dom.BaseURL] = struct{}{}
		}
	}
	return d
}

// empty 表示没有配置任何组。
func (d domainSet) empty() bool {
	return len(d.groups) == 0
}

// needsUserGroup 为真时拉取侧需要查用户组才能定组（仅有缺省组时可省一次读库）。
func (d domainSet) needsUserGroup() bool {
	return len(d.byUserGroup) > 0
}

// groupFor 按用户组返回对应组；未命中则用缺省组；组内无域名时返回 nil。
func (d domainSet) groupFor(userGroupID *uuid.UUID) *DomainGroup {
	var idx int
	if userGroupID != nil {
		if i, ok := d.byUserGroup[*userGroupID]; ok {
			idx = i
		} else if d.defaultIdx >= 0 {
			idx = d.defaultIdx
		} else {
			return nil
		}
	} else if d.defaultIdx >= 0 {
		idx = d.defaultIdx
	} else {
		return nil
	}
	if idx < 0 || idx >= len(d.groups) || len(d.groups[idx].Domains) == 0 {
		return nil
	}
	return &d.groups[idx]
}

// hostBaseURL 把请求 Host 头规范后匹配已登记基址。
func (d domainSet) hostBaseURL(host string) (string, bool) {
	h := strings.TrimSpace(host)
	h = strings.ToLower(h)
	if strings.HasSuffix(h, ":443") {
		h = strings.TrimSuffix(h, ":443")
	}
	h = strings.TrimSuffix(h, ".")
	if h == "" {
		return "", false
	}
	base := "https://" + h
	if _, ok := d.known[base]; ok {
		return base, true
	}
	return "", false
}

// baseURLsOf 返回组内全部基址（按组内顺序）。
func baseURLsOf(g *DomainGroup) []string {
	if g == nil {
		return nil
	}
	out := make([]string, len(g.Domains))
	for i, d := range g.Domains {
		out[i] = d.BaseURL
	}
	return out
}

// pickWeighted 按有效权重随机选一个基址；scores 为 nil 时分数当 1。
func pickWeighted(domains []Domain, scores map[string]float64) string {
	if len(domains) == 0 {
		return ""
	}
	if len(domains) == 1 {
		return domains[0].BaseURL
	}
	total := 0.0
	weights := make([]float64, len(domains))
	for i, d := range domains {
		w := d.Weight
		if w < 1 {
			w = 1
		}
		weights[i] = float64(w) * scoreOf(scores, d.BaseURL)
		total += weights[i]
	}
	if total <= 0 {
		return domains[0].BaseURL
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1<<53))
	if err != nil {
		return domains[0].BaseURL
	}
	x := float64(n.Int64()) / float64(1<<53) * total
	var acc float64
	for i, w := range weights {
		acc += w
		if x < acc {
			return domains[i].BaseURL
		}
	}
	return domains[len(domains)-1].BaseURL
}

// stableBaseURL 返回组内基础权重最大的基址，同权重取下标小的。
func stableBaseURL(g *DomainGroup) string {
	if g == nil || len(g.Domains) == 0 {
		return ""
	}
	best := 0
	for i := 1; i < len(g.Domains); i++ {
		if g.Domains[i].Weight > g.Domains[best].Weight {
			best = i
		}
	}
	return g.Domains[best].BaseURL
}

// domainGroupFor 解析用户应使用的订阅域名组（来自 extras 快照）。
func (s *Service) domainGroupFor(ctx context.Context, ex *extrasSnapshot, userID uuid.UUID) (*DomainGroup, error) {
	if ex == nil || ex.domains.empty() {
		return nil, nil
	}
	var ug *uuid.UUID
	if ex.domains.needsUserGroup() {
		id, err := s.users.UserGroupID(ctx, userID)
		if err != nil {
			return nil, err
		}
		ug = id
	}
	return ex.domains.groupFor(ug), nil
}
