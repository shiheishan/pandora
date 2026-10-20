package service

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/safehttp"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

// 站点对外根地址（主域名）：邮件链接、支付回调与回跳、订阅链接兜底、canonical、Telegram webhook、内容里的 {{siteUrl}} 都只经 SiteURL 读。
// 后台存的 site.url 优先，空就回落配置的 public_base_url。nginx、证书、节点连面板的地址只看配置，不跟着变。
//
// 只收 scheme://host[:port]，不支持子路径：门户与后台都在根 / 下发、hash 路由，回调、订阅、webhook 的路径都从根拼；
// 带子路径要 nginx 一起改前缀，而 nginx 不跟这个设置走，填了子路径只会让所有链接 404。

// SiteURLMaxLen 是主域名的长度上限。
const SiteURLMaxLen = 200

const (
	// siteURLTTL 是主域名快照的有效期。本进程的写在提交后立即失效快照；worker 等别的进程靠它兜底，最多晚这么久看到新值。
	siteURLTTL = 30 * time.Second
	// siteURLLoadTimeout 是一次读主域名设置的上限。
	siteURLLoadTimeout = 2 * time.Second
	// siteURLProbeTimeout 是保存前连通核对的总时长。
	siteURLProbeTimeout = 5 * time.Second
	// siteURLProbePath 是连通核对请求的门户公开接口：本面板一定有、不要登录、回 JSON。
	siteURLProbePath = "/v1/site-config"
)

// 主域名字段的错误文案（字段名 url），前端假后端同字。
const (
	siteURLMsgFormat = "请填写完整的地址，如 https://panel.example.com，不带路径和查询串"
	siteURLMsgHTTPS  = "主域名必须使用 https"
	siteURLMsgProbe  = "这个地址打不开本面板"
)

// SiteURLChangeListener 在主域名（site.url）改动提交后被同步调用：notify 据此用新地址重新注册 Telegram webhook。
// 失败由监听方自己记日志，不影响已提交的设置。
type SiteURLChangeListener interface{ SiteURLChanged(ctx context.Context) }

// siteURLValue 是库里的主域名设置（已规整，不带末尾 /；空 = 没设）。
type siteURLValue struct {
	stored   string // site.url
	previous string // site.url_previous：上一次改之前的生效值，旧单的回调与旧页面的人机验证还认它
}

// siteURLState 是主域名的配置值与进程内快照。
type siteURLState struct {
	// fallback 是配置的 public_base_url，规整成以 / 结尾；空 = 没配。dev 为 true（开发环境）时主域名也收 http、连通核对允许内网。
	fallback string
	dev      bool
	listener SiteURLChangeListener
	// probeClient 只给测试换；nil 用 safehttp 的共用客户端。
	probeClient *http.Client

	mu    sync.Mutex
	gen   uint64
	val   *siteURLValue
	exp   time.Time
	group singleflight.Group
}

// UseSiteURLConfig 注入配置的 public_base_url 与是否开发环境（启动装配时调用，不并发安全）。是配置值，不是端口，不进 MissingPorts。
func (s *Service) UseSiteURLConfig(publicBaseURL string, dev bool) {
	s.su.fallback = normalizeFallbackURL(publicBaseURL)
	s.su.dev = dev
}

// UseSiteURLChangeListener 接上主域名变更的监听方（启动装配时调用，不并发安全）。
func (s *Service) UseSiteURLChangeListener(l SiteURLChangeListener) { s.su.listener = l }

// normalizeFallbackURL 规整配置值：以 / 结尾（如 https://panel.example.test/）；没配或不是 http(s) 绝对地址时为空。
func normalizeFallbackURL(raw string) string {
	u := strings.TrimSpace(raw)
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	if parsed.Host == "" {
		return ""
	}
	path := strings.TrimRight(parsed.Path, "/")
	return scheme + "://" + parsed.Host + path + "/"
}

// normalizeSiteURL 校验并规整后台填的主域名：只收 http(s) 绝对地址（allowHTTP 为 false 时只收 https），
// 有主机名，不带账号、查询串、片段，路径只能空或 /；主机名转小写、去掉末尾的点，去掉默认端口。空串原样返回。
// 不合法时 msg 是给 url 字段的错误文案。
func normalizeSiteURL(raw string, allowHTTP bool) (norm, msg string) {
	v := strings.TrimSpace(raw)
	if v == "" {
		return "", ""
	}
	if len(v) > SiteURLMaxLen || strings.ContainsAny(v, "?#") {
		return "", siteURLMsgFormat
	}
	for _, r := range v {
		if r < 32 || r == 127 || unicode.IsSpace(r) {
			return "", siteURLMsgFormat
		}
	}
	u, err := url.Parse(v)
	if err != nil || u.Opaque != "" || u.User != nil || u.Host == "" {
		return "", siteURLMsgFormat
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !allowHTTP {
			return "", siteURLMsgHTTPS
		}
	default:
		return "", siteURLMsgFormat
	}
	if u.Path != "" && u.Path != "/" {
		return "", siteURLMsgFormat
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", siteURLMsgFormat
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", siteURLMsgFormat
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	out := scheme + "://" + host
	if port != "" {
		out += ":" + port
	}
	return out, ""
}

func invalidSiteURL(msg string) error { return errs.Invalid(map[string]string{"url": msg}) }

// SiteURL 返回站点对外根地址（当前生效的主域名），以 / 结尾（如 https://panel.example.test/）：
// 后台设了 site.url 用它，否则用配置的 public_base_url；都没有时为空。
// 读快照（本进程的写提交后立即失效，别的进程最多晚 siteURLTTL）；读库失败时回落配置值并 Warn。
func (s *Service) SiteURL(ctx context.Context) string {
	return s.effectiveSiteURL(s.siteURLs(ctx).stored)
}

// SiteURLAliases 返回本站认的全部根地址（都以 / 结尾，去重，当前生效的在最前）：当前生效值、上一个生效值、配置值。
// 支付回调验签（改域名前建的发票按旧地址签名）、回跳地址、人机验证核对令牌来源（旧域名上打开的页面）用它。
func (s *Service) SiteURLAliases(ctx context.Context) []string {
	v := s.siteURLs(ctx)
	var out []string
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			out = append(out, u)
		}
	}
	add(s.effectiveSiteURL(v.stored))
	if v.previous != "" {
		add(v.previous + "/")
	}
	add(s.su.fallback)
	return out
}

// SiteURLFallback 是配置的 public_base_url（不带末尾 /）：后台主域名一栏留空时用它，给后台显示。
func (s *Service) SiteURLFallback() string { return strings.TrimRight(s.su.fallback, "/") }

// effectiveSiteURL 把存的值换成生效值（以 / 结尾）。
func (s *Service) effectiveSiteURL(stored string) string {
	if stored != "" {
		return stored + "/"
	}
	return s.su.fallback
}

// siteURLs 读主域名快照；过期或失效后重读，同一纪元的并发读合并成一次。读库失败返回空值（即回落配置值），不缓存失败。
func (s *Service) siteURLs(ctx context.Context) siteURLValue {
	st := &s.su
	st.mu.Lock()
	if st.val != nil && s.now().Before(st.exp) {
		v := *st.val
		st.mu.Unlock()
		return v
	}
	gen := st.gen
	st.mu.Unlock()

	// 键带上纪元：失效之后来的调用不会挂到失效前开始的那趟读库上拿旧值。读库不跟随第一个调用者的取消。
	got, err, _ := st.group.Do(strconv.FormatUint(gen, 10), func() (any, error) {
		lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), siteURLLoadTimeout)
		defer cancel()
		vals, err := s.store.Values(lctx, []string{keySiteURL, keySiteURLPrevious})
		if err != nil {
			return siteURLValue{}, err
		}
		v := siteURLValue{
			stored:   s.decodeStoredSiteURL(ctx, vals, keySiteURL),
			previous: s.decodeStoredSiteURL(ctx, vals, keySiteURLPrevious),
		}
		st.mu.Lock()
		if st.gen == gen {
			st.val = &v
			st.exp = s.now().Add(siteURLTTL)
		}
		st.mu.Unlock()
		return v, nil
	})
	if err != nil {
		plog.From(ctx).Warn("settings: 读主域名设置失败，按配置的 public_base_url", zap.Error(err))
		return siteURLValue{}
	}
	return got.(siteURLValue)
}

// decodeStoredSiteURL 解出库里存的一个地址；缺行为空。值被手工改坏（或开发环境存的 http 到了生产）时 Warn 并按空。
func (s *Service) decodeStoredSiteURL(ctx context.Context, vals map[string]json.RawMessage, key string) string {
	raw, err := decodeOr(vals, key, "")
	if err == nil {
		norm, msg := normalizeSiteURL(raw, s.su.dev)
		if msg == "" {
			return norm
		}
	}
	plog.From(ctx).Warn("settings: 主域名设置的值非法，按空", zap.String("key", key))
	return ""
}

// invalidateSiteURL 清掉本进程的主域名快照并作废正在进行的读。
func (s *Service) invalidateSiteURL() {
	s.su.mu.Lock()
	s.su.gen++
	s.su.val = nil
	s.su.mu.Unlock()
}

// prepareSiteURL 是 SetSite 在事务外做的那半：校验规整新值；新值非空且与库里当前存的不同时，
// 先 GET 一次 <新地址>/v1/site-config 核对它确实通到本面板（配错一个字母，支付回调和邮件链接就全断了）。
// 回落到空（恢复用配置）不核对。返回规整后的新值。
func (s *Service) prepareSiteURL(ctx context.Context, raw string) (string, error) {
	norm, msg := normalizeSiteURL(raw, s.su.dev)
	if msg != "" {
		return "", invalidSiteURL(msg)
	}
	if norm == "" {
		return "", nil
	}
	vals, err := s.store.Values(ctx, []string{keySiteURL})
	if err != nil {
		return "", err
	}
	if cur, _ := decodeOr(vals, keySiteURL, ""); cur == norm {
		return norm, nil
	}
	if !s.probeSiteURL(ctx, norm) {
		return "", invalidSiteURL(siteURLMsgProbe)
	}
	return norm, nil
}

// probeSiteURL 经防 SSRF 的出站客户端 GET <base>/v1/site-config：5 秒，不跟随跳转，必须 200、Content-Type 是 JSON、
// 是带 registration_mode 的对象（本面板 site-config 的形状）。生产不许连内网与回环（safehttp 在拨号时按真实 IP 拦）。
func (s *Service) probeSiteURL(ctx context.Context, base string) bool {
	log := plog.From(ctx)
	pctx, cancel := context.WithTimeout(ctx, siteURLProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, base+siteURLProbePath, nil)
	if err != nil {
		log.Warn("settings: 主域名连通核对：请求构造失败", zap.Error(plog.StripURL(err)))
		return false
	}
	req.Header.Set("Accept", "application/json")
	client := s.su.probeClient
	if client == nil {
		client = safehttp.Client(s.su.dev)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Warn("settings: 主域名连通核对：连不上", zap.Error(plog.StripURL(err)))
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Warn("settings: 主域名连通核对：状态码不是 200", zap.Int("status", resp.StatusCode))
		return false
	}
	if mt, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		log.Warn("settings: 主域名连通核对：响应不是 JSON")
		return false
	}
	body, err := safehttp.ReadBody(resp.Body)
	if err != nil {
		log.Warn("settings: 主域名连通核对：读响应失败", zap.Error(err))
		return false
	}
	var probe struct {
		RegistrationMode *string `json:"registration_mode"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.RegistrationMode == nil {
		log.Warn("settings: 主域名连通核对：响应不是本面板的 site-config")
		return false
	}
	return true
}

// siteURLChanged 在主域名改动的事务提交后：先失效快照，再通知监听方（它读到的已是新值）；回滚不做。
// ctx 里没有事务时 AfterCommit 会立即执行。
func (s *Service) siteURLChanged(ctx context.Context) {
	tx.AfterCommit(ctx, "settings:site_url_saved", func(ctx context.Context) {
		s.invalidateSiteURL()
		if s.su.listener != nil {
			s.su.listener.SiteURLChanged(ctx)
		}
	})
}
