package service

// 订阅拉取：全站唯一一个不登录、来自公网、任何人都能访问的业务接口。
//
// 认证不通过的请求（前缀或令牌不对、凭据失效、订阅已停用、主人账号停用）一律回 ErrPullDenied，handler 回伪装 404，
// 与「这个路径不存在」无法区分。唯一的例外是「令牌有效、订阅过期、续费窗口没关」：回一条到期提示节点
// （持有有效令牌的人本来就知道链接是真的，告诉他过期了不泄露新信息）。
//
// 认证从不缓存：换链接或吊销后，旧链接下一次拉取就失效。前缀生成后不变，读到一次就留在进程里。

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/render"
	"github.com/aegispanel/aegis/internal/module/subscription/repo"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
)

const (
	// minTokenLen 以下的令牌连库都不查：扫描器的绝大多数试探止于此。
	minTokenLen = 16
	// 客户端回来拉取的间隔（小时）：正常 12；过期 1，续费后很快拉回真实线路。
	updateIntervalHours        = 12
	expiredUpdateIntervalHours = 1
	// ownerActive 是订阅主人账号的正常状态（identity 的 users.status）；其余（停用、封禁）拉不到任何东西。
	ownerActive = "active"
)

// tokenExts 是客户端常给链接加的扩展名（让自己认得出格式），比对前去掉。
var tokenExts = []string{".yaml", ".yml", ".json", ".txt", ".conf"}

// pullVerdict 是一次拉取的认证结论。
type pullVerdict int

const (
	pullDenied pullVerdict = iota
	pullLive
	pullExpired
)

// verdictOf 判定按哈希取回的凭据与订阅能不能拉：
//   - 凭据 active，或已吊销但还在宽限期内（grace_until 晚于 now），才算有效；否则 denied；
//   - 凭据有效时按订阅的状态口径：生效 → live；可救回（过期、续费窗口没关）→ expired；已停用 → denied。
func verdictOf(credStatus string, graceUntil *time.Time, st State, now time.Time) pullVerdict {
	usable := credStatus == "active" || (graceUntil != nil && graceUntil.After(now))
	if !usable {
		return pullDenied
	}
	switch st {
	case StateLive:
		return pullLive
	case StateRevivable:
		return pullExpired
	}
	return pullDenied
}

// Pull 完成一次订阅拉取：认证、渲染、限流、记日志。
//
// 返回的错误：ErrPullDenied（handler 回伪装 404）；errs.RateLimited（带 RetryAfterSeconds，回 429）；
// ErrDeliveryUnavailable（分发依赖或线路来源没装配，回 503）；其余是读库、取线路之类的内部错误（handler 记 ERROR、回伪装 404）。
func (s *Service) Pull(ctx context.Context, req PullRequest) (PullResult, error) {
	d, err := s.needDelivery()
	if err != nil {
		return PullResult{}, err
	}
	ua := reqmeta.From(ctx).UserAgent
	family := render.UAFamily(ua)
	format := render.DetectFormat(ua, req.Target)
	token := req.Token
	for _, ext := range tokenExts {
		token = strings.TrimSuffix(token, ext)
	}

	ok, err := s.matchPrefix(ctx, d, req.Prefix)
	if err != nil {
		return PullResult{}, err
	}
	if !ok || len(token) < minTokenLen {
		s.recordUnauthenticated(ctx, d, family)
		return PullResult{}, ErrPullDenied
	}
	sum := sha256.Sum256([]byte(token))
	now := s.clock()
	row, found, err := s.repo.PullByTokenHash(ctx, sum[:], now)
	if err != nil {
		return PullResult{}, subErr(err)
	}
	verdict := pullDenied
	// 库里按唯一索引等值查到之后，再做一次定时安全比较
	if found && hmac.Equal(row.TokenHash, sum[:]) {
		verdict = verdictOf(row.CredentialStatus, row.GraceUntil, StateAt(row.Status, row.PeriodEnd, row.RenewalClosedAt, now), now)
	}
	if verdict == pullDenied {
		s.recordUnauthenticated(ctx, d, family)
		return PullResult{}, ErrPullDenied
	}
	// 主人账号停用、封禁：按「不在服务」处理（与节点名单不收他的订阅同一口径），过期提示也不给；
	// 凭据不吊销，恢复后原链接照常可用。线路要的用户组也取自这一次读取，不再另读
	owner, err := d.Owners.Get(ctx, row.UserID)
	if err != nil {
		return PullResult{}, err
	}
	if owner.Status != ownerActive {
		s.recordUnauthenticated(ctx, d, family)
		return PullResult{}, ErrPullDenied
	}
	if verdict == pullLive && s.nodes == nil {
		return PullResult{}, ErrDeliveryUnavailable
	}

	credID, subID := row.CredentialID, row.SubscriptionID
	entry := fetchEntry{credID: &credID, subID: &subID, format: format, family: family}
	// fail 在认证通过之后出错时记一条 error 日志，再把错误交回（handler 记 ERROR、回伪装 404）
	fail := func(err error) (PullResult, error) {
		s.logFetch(ctx, d, entry.with(FetchError, 0, 0))
		return PullResult{}, err
	}
	cfg, err := d.Settings.SubscriptionDelivery(ctx)
	if err != nil {
		return fail(err)
	}
	site, err := s.settings.Site(ctx)
	if err != nil {
		return fail(err)
	}
	plans, err := s.catalog.PlansByIDs(ctx, []uuid.UUID{row.PlanID})
	if err != nil {
		return fail(err)
	}
	label := ""
	if row.Label != nil {
		label = *row.Label
	}
	res := PullResult{
		Userinfo:           userinfoOf(row),
		ContentDisposition: render.ContentDisposition(render.ProfileName(site.Name, label, plans[row.PlanID].Name)),
	}

	if verdict == pullExpired {
		notice := render.ExpiredNotice(row.PeriodEnd, siteLocation(site.Timezone))
		res.Body, res.ContentType = render.RenderExpired(format, notice)
		res.UpdateIntervalHours = expiredUpdateIntervalHours
		res.Expired = true
		res.WebPageURL = renewalPageURL(d.siteBase(ctx), subID)
		if err := s.allowFetch(ctx, d, cfg.FetchLimitPerHour, entry, FetchExpired); err != nil {
			return PullResult{}, err
		}
		if err := s.recordFetch(ctx, d, entry.with(FetchExpired, 0, len(res.Body))); err != nil {
			return PullResult{}, err
		}
		return res, nil
	}

	nodes, err := s.nodes.DeliverableNodes(ctx, NodeQuery{
		SubscriptionID: subID, UserID: row.UserID, PlanID: row.PlanID, PlanVersionID: row.PlanVersionID, UserGroupID: owner.UserGroupID,
	})
	if err != nil {
		return fail(err)
	}
	// 订阅附加设置与订阅域名分组走进程内快照（30 秒过期），不每次拉取都查库
	ex, err := s.extras.get(ctx, d.Extras, extrasSource{s}, now)
	if err != nil {
		return fail(err)
	}
	// 按 UA 选客户端内核认得的写法。nodes 可能来自 node 模块的进程内缓存、被别的请求共享：只读不改（前处理只改副本）
	body, contentType, count := render.RenderRequest(liveRequest(liveInput{
		Format: format, Nodes: nodes, Row: row, UA: ua, Options: d.Render, Extras: ex,
		Types: req.Types, Filter: req.Filter, Loc: siteLocation(site.Timezone),
		SubscribeURL: subscribeURLFor(ex, owner.UserGroupID, d.siteBase(ctx), req.Prefix, token),
		Now:          now,
	}))
	res.Body, res.ContentType = body, contentType
	res.UpdateIntervalHours = updateIntervalHours
	// 读取与渲染都完成后才占限流额度：失败的请求不占
	if err := s.allowFetch(ctx, d, cfg.FetchLimitPerHour, entry, FetchOK); err != nil {
		return PullResult{}, err
	}
	if err := s.recordFetch(ctx, d, entry.with(FetchOK, count, len(body))); err != nil {
		return PullResult{}, err
	}
	// 经令牌主人所在组的订阅域名到达的成功拉取，是这个域名「能连上」的信号；别组的域名（Host 可伪造）不加分。
	// 按拉取者 IP 的省份与运营商同时记三级；查归属地在凭据去重通过之后才做（同一凭据对同一域名每小时一次）。
	// 进程内攒批写 Redis，不多一次往返
	if base, ok := ex.domains.hostBaseURL(req.Host); ok && groupHas(ex.domains.groupFor(owner.UserGroupID), base) {
		s.scores.NoteSuccess(ctx, base, credID.String(), func() domainRegion { return s.regionOf(reqmeta.From(ctx).IP) }, now)
	}
	return res, nil
}

// allowFetch 按凭据每小时的上限限流（成功与过期提示各算各的）；超限记一条 rate_limited 日志并交回 429。
// Redis 不可用时放行（普通层，见 pkg/ratelimit）。
func (s *Service) allowFetch(ctx context.Context, d *Delivery, perHour int, e fetchEntry, result string) error {
	rule := ratelimit.FixedWindow("sub_fetch_"+result, perHour, time.Hour)
	err := d.Limiter.Allow(ctx, rule, e.credID.String())
	if err == nil {
		return nil
	}
	var ee *errs.Error
	if errors.As(err, &ee) && ee.Code == errs.RateLimited {
		s.logFetch(ctx, d, e.with(FetchRateLimited, 0, 0))
	}
	return err
}

// userinfoOf 算 Subscription-Userinfo：总量 = 本期套餐额度（不限或没有配额行按 0）+ 这一行上送的量；
// 全部计入 download（我们只记总量，编一个上下行比例会让用户看到假数据）；expire 是到期时刻。
func userinfoOf(row repo.PullRow) Userinfo {
	u := Userinfo{Expire: row.PeriodEnd.Unix()}
	var limit int64
	if row.TrafficLimit != nil {
		limit = *row.TrafficLimit
	}
	if row.TrafficConsumed != nil {
		u.Download = *row.TrafficConsumed
	}
	if row.TrafficBonus != nil && row.TrafficLimit != nil {
		limit += *row.TrafficBonus
	}
	u.Total = limit
	return u
}

// pathPrefix 取订阅路径前缀：进程里有就用，没有就经 settings 读（没有时 settings 生成并落库）后留下。
func (s *Service) pathPrefix(ctx context.Context, d *Delivery) (string, error) {
	if p := s.prefix.Load(); p != nil {
		return *p, nil
	}
	cfg, err := d.Settings.SubscriptionDelivery(ctx)
	if err != nil {
		return "", err
	}
	p := cfg.PathPrefix
	s.prefix.Store(&p)
	return p, nil
}

// matchPrefix 用定时安全比较核对路径前缀：前缀不保密，但逐字节比对会泄露「猜对了几位」。
func (s *Service) matchPrefix(ctx context.Context, d *Delivery, prefix string) (bool, error) {
	if prefix == "" {
		return false, nil
	}
	want, err := s.pathPrefix(ctx, d)
	if err != nil {
		return false, err
	}
	return want != "" && hmac.Equal([]byte(want), []byte(prefix)), nil
}

// linkURL 拼订阅链接：公开地址 / 前缀 / 令牌。
func linkURL(base, prefix, token string) string {
	return strings.TrimRight(base, "/") + "/" + prefix + "/" + token
}

// renewalPageURL 是门户里这一份的续费页（门户在根 / 下发，hash 路由）。
func renewalPageURL(base string, subID uuid.UUID) string {
	return strings.TrimRight(base, "/") + "/#/checkout?renew=" + subID.String()
}

// siteLocation 是站点时区；名字无效时退回 UTC（站点设置写入时已校验，这里只兜底）。
func siteLocation(name string) *time.Location {
	if loc, err := time.LoadLocation(name); err == nil && name != "" {
		return loc
	}
	return time.UTC
}
