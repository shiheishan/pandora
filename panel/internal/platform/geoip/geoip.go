// Package geoip 把 IP 解析成风控看得懂的画像：地理位置、运营商、网络性质。
//
// # 为什么用离线库而不是在线接口
//
// 风控要给每一条登录、注册、订阅拉取都标上归属地。走在线接口意味着每条
// 日志一次外部请求——延迟、限流、对方挂了我们跟着挂，而且等于把用户的
// 真实 IP 持续送给第三方。离线库一次加载常驻内存，查询是纳秒级的纯计算。
//
// # 精度边界（重要，别当成绝对真相）
//
// 中国 IP 的「省 + 市 + 运营商」基本可靠，实测北京电信、教育网都对得上。
// 但云厂商的 IP 段城市经常不准——机房归属变更频繁，各家库的更新节奏不
// 一样。所以判断「是不是机房 IP」不看城市，看 ISP 名字里的厂商关键词，
// 那个比地理位置稳定得多。
package geoip

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"
	"sync"

	"github.com/lionsoul2014/ip2region/binding/golang/xdb"
)

// NetworkKind 是这个 IP 的网络性质。风控关心的往往不是「在哪」，而是
// 「这是什么样的网络」——家宽换 IP 很正常，机房 IP 登录用户账号就可疑。
type NetworkKind string

const (
	KindUnknown     NetworkKind = ""            // 判不出来
	KindResidential NetworkKind = "residential" // 住宅宽带
	KindDatacenter  NetworkKind = "datacenter"  // 机房 / 云厂商
	KindEducation   NetworkKind = "education"   // 教育网
	KindMobile      NetworkKind = "mobile"      // 移动数据网络
	KindLoopback    NetworkKind = "loopback"    // 本机
	KindPrivate     NetworkKind = "private"     // 内网
)

// Location 是一次解析的结果。
type Location struct {
	Country string      `json:"country,omitempty"`
	Region  string      `json:"region,omitempty"` // 省 / 州
	City    string      `json:"city,omitempty"`
	ISP     string      `json:"isp,omitempty"`
	Kind    NetworkKind `json:"kind,omitempty"`
	// Display 是给后台直接展示的一行文本，例如「中国 北京 北京市 电信」。
	// 各字段拼接的规则放在这里而不是前端：同一份数据会出现在后台列表、
	// 导出的 CSV 和告警通知里，散在三处迟早拼得不一样。
	Display string `json:"display,omitempty"`
}

// Resolver 解析 IP。零值不可用，用 Open 构造。
type Resolver struct {
	ipv4Searcher *xdb.Searcher
	ipv6Searcher *xdb.Searcher
	ipv4SearchMu sync.Mutex
	ipv6SearchMu sync.Mutex
	cacheMu      sync.RWMutex
	cache        map[netip.Addr]Location
	ipv6WarnOnce sync.Once
}

// ErrNoDatabase 表示没有配置数据库文件。调用方据此决定是降级（不标注
// 归属地照样记日志）还是直接报错——风控日志本身比归属地重要，不该因为
// 缺一个数据文件就不记了。
var ErrNoDatabase = errors.New("geoip: 未配置 ip2region 数据库")

// Open 加载 ip2region 的 xdb 文件。
func Open(path string) (*Resolver, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrNoDatabase
	}
	ipv4Searcher, err := xdb.NewWithFileOnly(xdb.IPv4, path)
	if err != nil {
		return nil, err
	}

	r := &Resolver{ipv4Searcher: ipv4Searcher, cache: make(map[netip.Addr]Location)}
	if ipv6Path := strings.TrimSpace(os.Getenv("AEGIS_GEOIP_IPV6_DB")); ipv6Path != "" {
		r.ipv6Searcher, err = xdb.NewWithFileOnly(xdb.IPv6, ipv6Path)
		if err != nil {
			ipv4Searcher.Close()
			return nil, fmt.Errorf("打开 IPv6 GeoIP 数据库 %q: %w", ipv6Path, err)
		}
	}
	return r, nil
}

func (r *Resolver) Close() {
	if r == nil {
		return
	}
	if r.ipv4Searcher != nil {
		r.ipv4SearchMu.Lock()
		r.ipv4Searcher.Close()
		r.ipv4SearchMu.Unlock()
	}
	if r.ipv6Searcher != nil {
		r.ipv6SearchMu.Lock()
		r.ipv6Searcher.Close()
		r.ipv6SearchMu.Unlock()
	}
}

// Lookup 解析一个 IP。nil Resolver 返回空结果而不是 panic：归属地是锦上
// 添花，没有它日志照记。
func (r *Resolver) Lookup(ip string) Location {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return Location{}
	}
	// 本机和内网先短路：查库只会得到一条无意义的记录，而这两类在风控里
	// 有明确含义（本机通常是服务端自己发起的请求）。
	if addr.IsLoopback() {
		return Location{Kind: KindLoopback, Display: "本机回环地址"}
	}
	if addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return Location{Kind: KindPrivate, Display: "内网地址"}
	}
	if r == nil {
		return Location{}
	}

	r.cacheMu.RLock()
	cached, ok := r.cache[addr]
	r.cacheMu.RUnlock()
	if ok {
		return cached
	}

	searcher, searchMu := r.searcherFor(addr)
	if searcher == nil {
		return Location{}
	}
	// xdb 的 file-only Searcher 共享 Seek 状态，不支持并发 Search。查询锁与
	// 缓存锁分离，避免一次磁盘查询阻塞其他 IP 的缓存命中。
	searchMu.Lock()
	raw, err := searcher.Search(addr.String())
	searchMu.Unlock()
	if err != nil {
		return Location{}
	}
	loc := parseRegion(raw)

	r.cacheMu.Lock()
	// 缓存无上限会被大量随机 IP 撑爆（爬虫扫描时尤其明显）。到顶就整个
	// 丢掉重来：这里存的是可重算的派生数据，简单清空比维护 LRU 划算。
	if len(r.cache) >= maxCacheEntries {
		r.cache = make(map[netip.Addr]Location, maxCacheEntries/2)
	}
	r.cache[addr] = loc
	r.cacheMu.Unlock()
	return loc
}

func (r *Resolver) searcherFor(addr netip.Addr) (*xdb.Searcher, *sync.Mutex) {
	if addr.Is4() {
		return r.ipv4Searcher, &r.ipv4SearchMu
	}
	if r.ipv6Searcher == nil {
		r.ipv6WarnOnce.Do(func() {
			slog.Warn("未配置 IPv6 GeoIP 数据库，IPv6 归属地查询将降级为空",
				"env", "AEGIS_GEOIP_IPV6_DB")
		})
		return nil, nil
	}
	return r.ipv6Searcher, &r.ipv6SearchMu
}

const maxCacheEntries = 50000

// parseRegion 拆 ip2region 的「国家|区域|省份|城市|ISP」五段格式。
// 缺失的段是字面量 "0"，不是空串——直接展示会变成一串没意义的零。
func parseRegion(raw string) Location {
	parts := strings.Split(raw, "|")
	get := func(i int) string {
		if i >= len(parts) {
			return ""
		}
		v := strings.TrimSpace(parts[i])
		if v == "0" || v == "内网IP" {
			return ""
		}
		return v
	}
	loc := Location{Country: get(0), Region: get(1), City: get(2), ISP: get(3)}
	loc.Kind = classify(loc.ISP)
	segs := make([]string, 0, 4)
	for _, s := range []string{loc.Country, loc.Region, loc.City, loc.ISP} {
		if s != "" {
			segs = append(segs, s)
		}
	}
	loc.Display = strings.Join(segs, " ")
	return loc
}

// classify 从 ISP 名字判断网络性质。
//
// 为什么按名字而不按 ASN：ASN 到网络性质需要另一份持续维护的映射表，而
// 云厂商在 ISP 字段里的名字相当稳定（"阿里云"就是"阿里云"）。名字匹配
// 覆盖不到的会落到 unknown，那是诚实的结果，比用一份过期的 ASN 表硬猜好。
func classify(isp string) NetworkKind {
	if isp == "" {
		return KindUnknown
	}
	lower := strings.ToLower(isp)
	for _, kw := range datacenterKeywords {
		if strings.Contains(lower, kw) {
			return KindDatacenter
		}
	}
	for _, kw := range educationKeywords {
		if strings.Contains(lower, kw) {
			return KindEducation
		}
	}
	for _, kw := range mobileKeywords {
		if strings.Contains(lower, kw) {
			return KindMobile
		}
	}
	for _, kw := range residentialKeywords {
		if strings.Contains(lower, kw) {
			return KindResidential
		}
	}
	return KindUnknown
}

// 关键词都用小写匹配。机房这一类放在最前面判断：云厂商也会带「电信」
// 之类的字样（例如「电信云计算」），先判机房才不会被误归成住宅。
var (
	datacenterKeywords = []string{
		"阿里云", "腾讯云", "华为云", "百度云", "京东云", "ucloud", "青云",
		"aliyun", "alibaba", "tencent", "huawei cloud", "amazon", "aws",
		"google", "microsoft", "azure", "digitalocean", "linode", "vultr",
		"hetzner", "ovh", "cloudflare", "oracle", "idc", "数据中心",
		// 骨干网与主机托管商：它们的地址段基本都是机房出口，实测生产日志里
		// 的订阅拉取就来自 Cogent。漏掉这类会让机房流量被标成「判不出」。
		"cogent", "level3", "lumen", "zenlayer", "leaseweb", "choopa",
		"contabo", "scaleway", "colocation", "colo", "vps",
		"云计算", "服务器", "hosting", "datacenter", "data center",
	}
	educationKeywords   = []string{"教育网", "cernet", "education", "university", "大学", "学院"}
	mobileKeywords      = []string{"移动", "联通3g", "4g", "5g", "mobile", "cellular", "gprs"}
	residentialKeywords = []string{
		"电信", "联通", "铁通", "长城宽带", "广电", "有线通", "鹏博士",
		"telecom", "unicom", "broadband", "residential", "cable", "dsl",
	}
)
