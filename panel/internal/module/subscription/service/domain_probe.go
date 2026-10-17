// 门户探测订阅域名的票据、探测页数据与上报。为什么用独立探测页与票据：门户入口页对匿名访客一样，
// 不能把任何订阅域名放进它的 CSP（会公开只给某些组的域名、能从门户反查订阅域名）；探测页只对持票据的本人下发、
// CSP 只列本人所在组的域名。上报可伪造，防操纵见 domain_probe_scores.go；只收有生效中、且不是注册试用发的订阅的账号（试用订阅注册即有，不排除等于没有门槛）。

package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

const (
	// probeTicketTTL 是探测票据的有效期。
	probeTicketTTL = 5 * time.Minute
	// probeTicketVersion 是票据格式版本（签名内容的第一段）。
	probeTicketVersion = "v1"
	// probeTicketMaxLen 是票据串的长度上限（正常约 150 字符）。
	probeTicketMaxLen = 512
)

// errProbeTicket 是票据无效、过期、账号已没有生效的非试用订阅或所在组已变时的统一错误（不区分原因，不给试探的人线索）。
var errProbeTicket = errs.Newf(errs.Unauthorized, "探测票据无效或已过期")

// ProbeResult 是探测页对一个订阅域名的探测结果。
type ProbeResult struct {
	BaseURL string
	OK      bool
}

// probeTicket 是解开并验过签名的票据内容。
type probeTicket struct {
	UserID  uuid.UUID
	GroupFP string
	Exp     time.Time
	Nonce   string
}

// groupFingerprint 是组的「版本」——组 id 与组内域名（按顺序）任一变化，旧票据就作废。
func groupFingerprint(g *DomainGroup) string {
	if g == nil {
		return ""
	}
	h := sha256.Sum256([]byte(g.ID.String() + "\n" + strings.Join(baseURLsOf(g), "\n")))
	return hex.EncodeToString(h[:])[:16]
}

// signProbeTicket 签票据：base64url(payload) + "." + base64url(HMAC)，payload = 版本|账号|组指纹|过期 unix 秒|随机串。
func (s *Service) signProbeTicket(d *Delivery, t probeTicket) string {
	payload := probeTicketVersion + "|" + t.UserID.String() + "|" + t.GroupFP + "|" +
		strconv.FormatInt(t.Exp.Unix(), 10) + "|" + t.Nonce
	mac := d.Hasher.HMAC(crypto.InfoHMACSubProbe, []byte(payload))
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac)
}

// parseProbeTicket 解开并验签；格式、签名、版本、过期、有效期上限、随机串任一不对都回 errProbeTicket（不区分原因）。
func (s *Service) parseProbeTicket(d *Delivery, raw string, now time.Time) (probeTicket, error) {
	if len(raw) > probeTicketMaxLen {
		return probeTicket{}, errProbeTicket
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return probeTicket{}, errProbeTicket
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return probeTicket{}, errProbeTicket
	}
	mac, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return probeTicket{}, errProbeTicket
	}
	payload := string(payloadBytes)
	expect := d.Hasher.HMAC(crypto.InfoHMACSubProbe, payloadBytes)
	if !hmac.Equal(mac, expect) {
		return probeTicket{}, errProbeTicket
	}
	segs := strings.Split(payload, "|")
	if len(segs) != 5 {
		return probeTicket{}, errProbeTicket
	}
	if segs[0] != probeTicketVersion {
		return probeTicket{}, errProbeTicket
	}
	uid, err := uuid.Parse(segs[1])
	if err != nil {
		return probeTicket{}, errProbeTicket
	}
	expUnix, err := strconv.ParseInt(segs[3], 10, 64)
	if err != nil {
		return probeTicket{}, errProbeTicket
	}
	if now.Unix() >= expUnix {
		return probeTicket{}, errProbeTicket
	}
	if expUnix > now.Add(probeTicketTTL).Unix()+60 {
		return probeTicket{}, errProbeTicket
	}
	nonce := segs[4]
	if len(nonce) != 16 {
		return probeTicket{}, errProbeTicket
	}
	for _, c := range nonce {
		isDigit := c >= '0' && c <= '9'
		isHexLower := c >= 'a' && c <= 'f'
		if !isDigit && !isHexLower {
			return probeTicket{}, errProbeTicket
		}
	}
	return probeTicket{
		UserID:  uid,
		GroupFP: segs[2],
		Exp:     time.Unix(expUnix, 0).UTC(),
		Nonce:   nonce,
	}, nil
}

// probeTicketGroup 验票并复核：先验签（不碰 IO），再查账号仍有生效的非试用订阅、当前所在组与签发时同一版本。
func (s *Service) probeTicketGroup(ctx context.Context, raw string) (probeTicket, *DomainGroup, error) {
	d, err := s.needDelivery()
	if err != nil {
		return probeTicket{}, nil, err
	}
	now := s.clock()
	t, err := s.parseProbeTicket(d, raw, now)
	if err != nil {
		return probeTicket{}, nil, err
	}
	live, err := s.repo.HasLiveNonTrialSubscription(ctx, t.UserID, now)
	if err != nil {
		return probeTicket{}, nil, err
	}
	if !live {
		return probeTicket{}, nil, errProbeTicket
	}
	ex, err := s.extras.get(ctx, d.Extras, extrasSource{s}, now)
	if err != nil {
		return probeTicket{}, nil, err
	}
	g, err := s.domainGroupFor(ctx, ex, t.UserID)
	if err != nil {
		return probeTicket{}, nil, err
	}
	if g == nil || len(g.Domains) == 0 || groupFingerprint(g) != t.GroupFP {
		return probeTicket{}, nil, errProbeTicket
	}
	return t, g, nil
}

// IssueProbeTicket 给当前门户用户签一张探测票据；没有生效的非试用订阅、所在组没有订阅域名时返回空串（不报错，门户就不探测）。
func (s *Service) IssueProbeTicket(ctx context.Context) (string, error) {
	p, err := principal(ctx)
	if err != nil {
		return "", err
	}
	d, err := s.needDelivery()
	if err != nil {
		return "", err
	}
	now := s.clock()
	live, err := s.repo.HasLiveNonTrialSubscription(ctx, p.UserID, now)
	if err != nil {
		return "", err
	}
	if !live {
		return "", nil
	}
	ex, err := s.extras.get(ctx, d.Extras, extrasSource{s}, now)
	if err != nil {
		return "", err
	}
	g, err := s.domainGroupFor(ctx, ex, p.UserID)
	if err != nil {
		return "", err
	}
	if g == nil || len(g.Domains) == 0 {
		return "", nil
	}
	var nonceBytes [8]byte
	if _, err := rand.Read(nonceBytes[:]); err != nil {
		return "", err
	}
	nonce := hex.EncodeToString(nonceBytes[:])
	t := probeTicket{
		UserID:  p.UserID,
		GroupFP: groupFingerprint(g),
		Exp:     now.Add(probeTicketTTL),
		Nonce:   nonce,
	}
	return s.signProbeTicket(d, t), nil
}

// ProbeFrameDomains 验票并返回票据所绑组的订阅域名（按组内顺序），给探测页生成 CSP 与探测列表。
func (s *Service) ProbeFrameDomains(ctx context.Context, ticket string) ([]string, error) {
	_, g, err := s.probeTicketGroup(ctx, ticket)
	if err != nil {
		return nil, err
	}
	return baseURLsOf(g), nil
}

// ReportProbeResults 验票后记探测结果：只认票据所绑组里的域名（先 NormalizeBaseURL，失败或不在组里的静默忽略，同一域名只取第一条），
// 按 BaseURL 字典序交给 scores.ApplyProbe（票据一次性、账号×域名 30 分钟一次、成功与失败都要同一小时 3 个不同账号、每小时净变动 ±0.5）。
// Redis 写失败只打 Warn、不报错（分数只是调权重的参考）。
func (s *Service) ReportProbeResults(ctx context.Context, ticket string, results []ProbeResult) error {
	t, g, err := s.probeTicketGroup(ctx, ticket)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return nil
	}
	seen := make(map[string]struct{})
	signals := make([]probeSignal, 0, len(results))
	for _, r := range results {
		norm, err := NormalizeBaseURL(r.BaseURL)
		if err != nil {
			continue
		}
		if !groupHas(g, norm) {
			continue
		}
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		signals = append(signals, probeSignal{BaseURL: norm, OK: r.OK})
	}
	if len(signals) == 0 {
		return nil
	}
	sort.Slice(signals, func(i, j int) bool { return signals[i].BaseURL < signals[j].BaseURL })
	if _, err := s.scores.ApplyProbe(ctx, t.UserID.String(), t.Nonce, signals, s.clock()); err != nil {
		plog.From(ctx).Warn("订阅域名探测分数写入失败", zap.Error(err))
	}
	return nil
}

// PortalOrigin 是门户的源（public_base_url 的 scheme://host[:port]，与浏览器发的 Origin 头、location.origin 同形：
// 小写、去掉 https 的 :443 与 http 的 :80），给 /v1/sub-probe 的 CORS 判断与探测页的自检用；
// 分发没装配、地址解析不了或不是 http(s) 时为空串（不回 ACAO，探测页不探也不报）。
func (s *Service) PortalOrigin() string {
	d, err := s.needDelivery()
	if err != nil {
		return ""
	}
	return portalOrigin(d.PublicBaseURL)
}

// portalOrigin 把 public_base_url 规整成浏览器的源形状；不合的回空串。
func portalOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if host == "" || (scheme != "https" && scheme != "http") {
		return ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}
