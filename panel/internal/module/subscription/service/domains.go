// 订阅域名分组：后台建若干组，每组若干带权重的 https 基址，组按用户组分配；
// 取代 settings 里的 subscription.link_base_urls（用户 2026-10-14 定）。
// 数据在 subscription 模块三张表（subscription_domain_groups、subscription_domains、
// subscription_domain_group_user_groups）。拉取侧经 pull_extras 的进程内快照读，30 秒过期；
// 本进程后台保存成功后立即作废快照。

package service

import (
	"context"
	"errors"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const (
	// DomainGroupsMax 是订阅域名组数量上限。
	DomainGroupsMax = 20
	// DomainsPerGroupMax 是每组域名数量上限。
	DomainsPerGroupMax = 10
	// DomainWeightMax 是单个域名基础权重上限。
	DomainWeightMax = 100
	// domainGroupNameMax 是组名称最大字符数（按 rune）。
	domainGroupNameMax = 40
	// domainBaseURLMaxLen 是规范化后基址的最大长度。
	domainBaseURLMaxLen = 255
	// ActionDomainsChanged 是后台保存订阅域名分组的审计动作。
	ActionDomainsChanged = "subscription.domains_changed"
	// ResourceSubscriptionDomains 是审计资源类型。
	ResourceSubscriptionDomains = "subscription_domains"
)

// Domain 是组内一个订阅域名的基址与基础权重。
type Domain struct {
	BaseURL string // 规范化后的 https://host（只走 443，不带端口）
	Weight  int
}

// DomainGroup 是后台读出或快照里的一组订阅域名。
type DomainGroup struct {
	ID           uuid.UUID
	Name         string
	IsDefault    bool
	Domains      []Domain
	UserGroupIDs []uuid.UUID
}

// DomainGroupInput 是后台保存时的一组输入（无 id，整份替换）。
type DomainGroupInput struct {
	Name         string
	IsDefault    bool
	Domains      []Domain
	UserGroupIDs []uuid.UUID
}

// DomainView 是后台列表里单个域名的展示（含分数与有效权重）。
type DomainView struct {
	BaseURL         string
	Weight          int
	Score           float64
	EffectiveWeight float64
}

// DomainGroupView 是后台列表里的一组。
type DomainGroupView struct {
	ID           uuid.UUID
	Name         string
	IsDefault    bool
	Domains      []DomainView
	UserGroupIDs []uuid.UUID
}

// DomainConfigView 是 GET/PUT 订阅域名配置的响应体（service 层形状）。
type DomainConfigView struct {
	Groups          []DomainGroupView
	ScoresAvailable bool
}

var domainHostnameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]([a-z0-9-]*[a-z0-9])?$`)

// NormalizeBaseURL 把用户输入规范成 https 基址（只写到域名，不带端口，只走 443）；失败返回 errors.New，文案见 sd-spec。
func NormalizeBaseURL(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	if len(s) > domainBaseURLMaxLen {
		return "", errors.New("长度不能超过 255")
	}
	if strings.Contains(s, "?") || strings.Contains(s, "#") {
		return "", errors.New("只能写到域名，不能带路径、参数")
	}
	u, err := url.Parse(s)
	if err != nil {
		return "", errors.New("格式不正确")
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return "", errors.New("必须以 https:// 开头")
	}
	if u.Host == "" {
		return "", errors.New("必须包含域名")
	}
	if u.User != nil {
		return "", errors.New("不能包含用户名或密码")
	}
	if u.Path != "" && u.Path != "/" {
		return "", errors.New("只能写到域名，不能带路径、参数")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("只能写到域名，不能带路径、参数")
	}
	host := strings.ToLower(u.Hostname())
	if net.ParseIP(host) != nil || strings.Contains(host, ":") || strings.Contains(host, "[") {
		return "", errors.New("只能写域名，不能写 IP 地址")
	}
	for _, r := range host {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '.' && r != '-' {
			return "", errors.New("域名只能含小写字母、数字、点和短横线（中文域名请写成 xn-- 开头的形式）")
		}
	}
	port := u.Port()
	if port != "" {
		if port != "443" {
			if _, err := strconv.Atoi(port); err != nil {
				return "", errors.New("格式不正确")
			}
			return "", errors.New("不能带端口（订阅域名只走 443）")
		}
	}
	if len(host) > 253 || host == "localhost" || strings.HasSuffix(host, ".localhost") || !domainHostnameRE.MatchString(host) {
		return "", errors.New("域名格式不正确")
	}
	return "https://" + host, nil
}

func invalidGroups(msg string) error {
	return errs.Invalid(map[string]string{"groups": msg})
}

// normalizeDomainGroups 校验并规范化整份保存输入。
func normalizeDomainGroups(in []DomainGroupInput) ([]DomainGroupInput, error) {
	if len(in) > DomainGroupsMax {
		return nil, invalidGroups("订阅域名组最多 20 个")
	}
	names := make([]string, 0, len(in))
	for i := range in {
		name := strings.TrimSpace(in[i].Name)
		if name == "" {
			return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：名称不能为空")
		}
		if utf8.RuneCountInString(name) > domainGroupNameMax {
			return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：名称最多 40 个字")
		}
		for j, prev := range names {
			if prev == name {
				return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：名称和第 " + strconv.Itoa(j+1) + " 组重复")
			}
		}
		names = append(names, name)
	}
	defaultCount := 0
	for _, g := range in {
		if g.IsDefault {
			defaultCount++
		}
	}
	if defaultCount > 1 {
		return nil, invalidGroups("只能有一个缺省组")
	}
	out := make([]DomainGroupInput, len(in))
	for i, g := range in {
		domains := g.Domains
		if len(domains) == 0 {
			return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：至少要有一个域名")
		}
		if len(domains) > DomainsPerGroupMax {
			return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：域名最多 10 个")
		}
		seen := make(map[string]struct{})
		normDomains := make([]Domain, len(domains))
		for k, d := range domains {
			base, err := NormalizeBaseURL(d.BaseURL)
			if err != nil {
				return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组第 " + strconv.Itoa(k+1) + " 个域名：" + err.Error())
			}
			if _, dup := seen[base]; dup {
				return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组：" + base + " 重复了")
			}
			seen[base] = struct{}{}
			w := d.Weight
			if w == 0 {
				w = 1
			}
			if w < 0 || w > DomainWeightMax {
				return nil, invalidGroups("第 " + strconv.Itoa(i+1) + " 组第 " + strconv.Itoa(k+1) + " 个域名：权重必须是 1–100 的整数")
			}
			normDomains[k] = Domain{BaseURL: base, Weight: w}
		}
		ugIDs := make([]uuid.UUID, 0, len(g.UserGroupIDs))
		for _, id := range g.UserGroupIDs {
			if id == uuid.Nil {
				continue
			}
			ugIDs = append(ugIDs, id)
		}
		sort.Slice(ugIDs, func(a, b int) bool { return ugIDs[a].String() < ugIDs[b].String() })
		out[i] = DomainGroupInput{
			Name:         names[i],
			IsDefault:    g.IsDefault,
			Domains:      normDomains,
			UserGroupIDs: ugIDs,
		}
	}
	assigned := make(map[uuid.UUID]struct{})
	for _, g := range out {
		for _, id := range g.UserGroupIDs {
			if _, dup := assigned[id]; dup {
				return nil, invalidGroups("同一个用户组只能分到一个域名组")
			}
			assigned[id] = struct{}{}
		}
	}
	return out, nil
}

// domainGroupsFromRows 把 repo 扁平行还原成按 position 排序的组列表。
func domainGroupsFromRows(rows repo.DomainConfigRows) []DomainGroup {
	groups := make([]DomainGroup, len(rows.Groups))
	for i, g := range rows.Groups {
		groups[i] = DomainGroup{
			ID:           g.ID,
			Name:         g.Name,
			IsDefault:    g.IsDefault,
			Domains:      []Domain{},
			UserGroupIDs: []uuid.UUID{},
		}
	}
	byID := make(map[uuid.UUID]int, len(groups))
	for i, g := range rows.Groups {
		byID[g.ID] = i
	}
	for _, d := range rows.Domains {
		idx, ok := byID[d.GroupID]
		if !ok {
			continue
		}
		groups[idx].Domains = append(groups[idx].Domains, Domain{BaseURL: d.BaseURL, Weight: d.Weight})
	}
	for _, a := range rows.Assigns {
		idx, ok := byID[a.GroupID]
		if !ok {
			continue
		}
		groups[idx].UserGroupIDs = append(groups[idx].UserGroupIDs, a.UserGroupID)
	}
	return groups
}

// rowsFromInputs 把校验后的输入转成 repo 整份替换行。
func rowsFromInputs(groups []DomainGroupInput) repo.DomainConfigRows {
	rows := repo.DomainConfigRows{
		Groups:  []repo.DomainGroupRow{},
		Domains: []repo.DomainRow{},
		Assigns: []repo.DomainAssignRow{},
	}
	for i, g := range groups {
		id := uuid.Must(uuid.NewV7())
		rows.Groups = append(rows.Groups, repo.DomainGroupRow{
			ID:        id,
			Name:      g.Name,
			IsDefault: g.IsDefault,
			Position:  i,
		})
		for j, d := range g.Domains {
			rows.Domains = append(rows.Domains, repo.DomainRow{
				GroupID:  id,
				BaseURL:  d.BaseURL,
				Weight:   d.Weight,
				Position: j,
			})
		}
		for _, ug := range g.UserGroupIDs {
			rows.Assigns = append(rows.Assigns, repo.DomainAssignRow{
				UserGroupID: ug,
				GroupID:     id,
			})
		}
	}
	return rows
}

func sameDomainList(a, b []Domain) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].BaseURL != b[i].BaseURL || a[i].Weight != b[i].Weight {
			return false
		}
	}
	return true
}

func sameUUIDList(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// sameDomainConfig 判断保存内容与库里是否完全一致（无变化则跳过写库与审计）。
func sameDomainConfig(old []DomainGroup, groups []DomainGroupInput) bool {
	if len(old) != len(groups) {
		return false
	}
	for i := range old {
		if old[i].Name != groups[i].Name || old[i].IsDefault != groups[i].IsDefault {
			return false
		}
		if !sameDomainList(old[i].Domains, groups[i].Domains) {
			return false
		}
		if !sameUUIDList(old[i].UserGroupIDs, groups[i].UserGroupIDs) {
			return false
		}
	}
	return true
}

func auditDomainGroups(groups []DomainGroupInput) []map[string]any {
	out := make([]map[string]any, len(groups))
	for i, g := range groups {
		domains := make([]map[string]any, len(g.Domains))
		for j, d := range g.Domains {
			domains[j] = map[string]any{"base_url": d.BaseURL, "weight": d.Weight}
		}
		ugStrs := make([]string, len(g.UserGroupIDs))
		for j, id := range g.UserGroupIDs {
			ugStrs[j] = id.String()
		}
		out[i] = map[string]any{
			"name":           g.Name,
			"is_default":     g.IsDefault,
			"domains":        domains,
			"user_group_ids": ugStrs,
		}
	}
	return out
}

func auditFromGroups(groups []DomainGroup) []map[string]any {
	out := make([]map[string]any, len(groups))
	for i, g := range groups {
		domains := make([]map[string]any, len(g.Domains))
		for j, d := range g.Domains {
			domains[j] = map[string]any{"base_url": d.BaseURL, "weight": d.Weight}
		}
		ugStrs := make([]string, len(g.UserGroupIDs))
		for j, id := range g.UserGroupIDs {
			ugStrs[j] = id.String()
		}
		out[i] = map[string]any{
			"name":           g.Name,
			"is_default":     g.IsDefault,
			"domains":        domains,
			"user_group_ids": ugStrs,
		}
	}
	return out
}

// AdminDomainConfig 返回后台订阅域名配置（直读库，含 Redis 分数）。
func (s *Service) AdminDomainConfig(ctx context.Context) (DomainConfigView, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return DomainConfigView{}, err
	}
	rows, err := s.domainConfigRows(ctx)
	if err != nil {
		return DomainConfigView{}, subErr(err)
	}
	groups := domainGroupsFromRows(rows)
	urls := make([]string, 0)
	for _, g := range groups {
		for _, d := range g.Domains {
			urls = append(urls, d.BaseURL)
		}
	}
	scores, scoresOK := s.scores.Read(ctx, urls, s.clock())
	viewGroups := make([]DomainGroupView, len(groups))
	for i, g := range groups {
		domViews := make([]DomainView, len(g.Domains))
		for j, d := range g.Domains {
			sc := scoreOf(scores, d.BaseURL)
			domViews[j] = DomainView{
				BaseURL:         d.BaseURL,
				Weight:          d.Weight,
				Score:           sc,
				EffectiveWeight: float64(d.Weight) * sc,
			}
		}
		viewGroups[i] = DomainGroupView{
			ID:           g.ID,
			Name:         g.Name,
			IsDefault:    g.IsDefault,
			Domains:      domViews,
			UserGroupIDs: g.UserGroupIDs,
		}
	}
	return DomainConfigView{Groups: viewGroups, ScoresAvailable: scoresOK}, nil
}

// AdminSaveDomainConfig 整份替换订阅域名分组。
func (s *Service) AdminSaveDomainConfig(ctx context.Context, in []DomainGroupInput) (DomainConfigView, error) {
	if _, err := adminPrincipal(ctx); err != nil {
		return DomainConfigView{}, err
	}
	groups, err := normalizeDomainGroups(in)
	if err != nil {
		return DomainConfigView{}, err
	}
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.repo.LockDomainConfig(ctx); err != nil {
			return subErr(err)
		}
		rows, err := s.domainConfigRows(ctx)
		if err != nil {
			return subErr(err)
		}
		old := domainGroupsFromRows(rows)
		if sameDomainConfig(old, groups) {
			return nil
		}
		if err := s.repo.ReplaceDomainConfig(ctx, rowsFromInputs(groups)); err != nil {
			if errors.Is(err, repo.ErrForeignKey) {
				return errs.Invalid(map[string]string{"groups": "有用户组不存在（可能刚被删除），刷新后重试"})
			}
			return subErr(err)
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionDomainsChanged,
			ResourceType: ResourceSubscriptionDomains,
			Data: map[string]any{
				"before": auditFromGroups(old),
				"after":  auditDomainGroups(groups),
			},
		})
	}); err != nil {
		return DomainConfigView{}, err
	}
	// 本进程的拉取快照立即作废，下一次拉取就用新的域名分组
	s.extras.invalidate()
	return s.AdminDomainConfig(ctx)
}
