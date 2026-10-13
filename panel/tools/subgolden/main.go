// Command subgolden 用旧订阅渲染实现生成新 render 包的 golden。
//
// P4 一次性工具：订阅渲染从 internal/domain/subscription 逐字节移植到
// internal/module/subscription/render，这里用旧实现出基线，新包的测试逐字节比对。
// 生成的 testdata 留在仓库里继续当回归基线；本工具 P7 随旧代码删除。
//
//	cd panel && go run ./tools/subgolden -out internal/module/subscription/render/testdata
//
// 读取一侧的 JSON 形状见 render/testdata_test.go，两边字段名必须一致。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/subscription"
)

// nodeJSON 是 nodes.json 的一项：协议配置已翻译成内核形状。
type nodeJSON struct {
	Group       string         `json:"group"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	TrafficRate float64        `json:"traffic_rate"`
	Config      map[string]any `json:"config"`
}

type templateJSON struct {
	GeositeURLPrefix string `json:"geosite_url_prefix"`
	GeoIPURLPrefix   string `json:"geoip_url_prefix"`
	BlockAds         bool   `json:"block_ads"`
}

type renderJSON struct {
	Name        string       `json:"name"`
	Format      string       `json:"format"`
	UA          string       `json:"ua"`
	Empty       bool         `json:"empty"`
	Singbox     templateJSON `json:"singbox"`
	File        string       `json:"file"`
	ContentType string       `json:"content_type"`
	Count       int          `json:"count"`
}

type expiredJSON struct {
	Name            string `json:"name"`
	Format          string `json:"format"`
	PeriodEnd       string `json:"period_end"`
	TZName          string `json:"tz_name"`
	TZOffsetSeconds int    `json:"tz_offset_seconds"`
	NilLocation     bool   `json:"nil_location"`
	Notice          string `json:"notice"`
	File            string `json:"file"`
	ContentType     string `json:"content_type"`
}

type profileJSON struct {
	Site               string `json:"site"`
	Label              string `json:"label"`
	Plan               string `json:"plan"`
	ProfileName        string `json:"profile_name"`
	ContentDisposition string `json:"content_disposition"`
}

type formatJSON struct {
	UA       string `json:"ua"`
	Explicit string `json:"explicit"`
	Format   string `json:"format"`
	Family   string `json:"family"`
}

type manifestJSON struct {
	ProxyUUID string        `json:"proxy_uuid"`
	Renders   []renderJSON  `json:"renders"`
	Expired   []expiredJSON `json:"expired"`
	Profiles  []profileJSON `json:"profiles"`
	Formats   []formatJSON  `json:"formats"`
}

const (
	uaSingboxCompat = "SFA/1.13.14 (1; sing-box 1.13.14; language zh_CN)"
	uaSingbox114    = "SFA/1.14.2 (614; sing-box 1.14.2; language zh_CN)"
)

var (
	defaultTemplate = templateJSON{}
	// 自定义模板：GeoIP 前缀前后各带一个空格，钉住去空白。
	customTemplate = templateJSON{
		GeositeURLPrefix: "https://mirror.example.test/geosite/",
		GeoIPURLPrefix:   " https://mirror.example.test/geoip/ ",
		BlockAds:         true,
	}
)

func main() {
	out := flag.String("out", "", "testdata 目录（如 internal/module/subscription/render/testdata）")
	flag.Parse()
	if *out == "" {
		log.Fatal("缺 -out")
	}
	goldenDir := filepath.Join(*out, "golden")
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		log.Fatal(err)
	}
	files := 0

	writeJSON(filepath.Join(*out, "nodes.json"), kernelNodes())
	files++

	m := manifestJSON{ProxyUUID: fixtureUUID}
	for _, r := range renderCases() {
		subscription.ConfigureSingboxTemplate(subscription.SingboxTemplate{
			GeositeURLPrefix: r.Singbox.GeositeURLPrefix,
			GeoIPURLPrefix:   r.Singbox.GeoIPURLPrefix,
			BlockAds:         r.Singbox.BlockAds,
		})
		var nodes []subscription.Node
		if !r.Empty {
			nodes = rawNodes()
		}
		body, ct, count := subscription.RenderForClient(subscription.Format(r.Format), nodes, fixtureUUID, r.UA)
		writeFile(filepath.Join(goldenDir, r.File), body)
		files++
		r.ContentType, r.Count = ct, count
		m.Renders = append(m.Renders, r)
	}
	subscription.ConfigureSingboxTemplate(subscription.SingboxTemplate{})

	for _, e := range expiredCases() {
		writeFile(filepath.Join(goldenDir, e.file), e.body)
		files++
		m.Expired = append(m.Expired, e.expiredJSON)
	}
	m.Profiles = profileCases()
	m.Formats = formatCases()

	writeJSON(filepath.Join(goldenDir, "manifest.json"), m)
	files++
	fmt.Printf("subgolden: 写了 %d 个文件到 %s\n", files, *out)
}

// rawNodes 每次都从夹具文本重新解一份（与旧代码从库里读出的形状一致），各次渲染互不影响。
func rawNodes() []subscription.Node {
	fx := fixtures()
	out := make([]subscription.Node, 0, len(fx))
	for _, f := range fx {
		var cfg map[string]any
		if err := json.Unmarshal([]byte(f.config), &cfg); err != nil {
			log.Fatalf("%s：夹具不是 JSON：%v", f.name, err)
		}
		out = append(out, subscription.Node{Name: f.name, Type: f.typ, Host: fixtureHost, Port: f.port, Config: cfg, TrafficRate: 1})
	}
	return out
}

// kernelNodes 是交给新 render 包的输入：经旧的同一张映射表翻译成内核形状。
func kernelNodes() []nodeJSON {
	fx := fixtures()
	raw := rawNodes()
	out := make([]nodeJSON, 0, len(fx))
	for i, f := range fx {
		typ := nodefabric.CanonicalNodeType(f.typ)
		out = append(out, nodeJSON{
			Group: f.group, Name: f.name, Type: typ, Host: fixtureHost, Port: f.port, TrafficRate: 1,
			Config: nodefabric.KernelConfig(typ, raw[i].Config),
		})
	}
	return out
}

func renderCases() []renderJSON {
	return []renderJSON{
		{Name: "clash", Format: "clash", File: "clash.yaml", Singbox: defaultTemplate},
		{Name: "clash-premium", Format: "clash-premium", File: "clash-premium.yaml", Singbox: defaultTemplate},
		{Name: "singbox-compat", Format: "singbox", UA: uaSingboxCompat, File: "singbox-compat.json", Singbox: defaultTemplate},
		{Name: "singbox-1.14", Format: "singbox", UA: uaSingbox114, File: "singbox-1.14.json", Singbox: defaultTemplate},
		{Name: "singbox-compat-custom", Format: "singbox", UA: uaSingboxCompat, File: "singbox-compat-custom.json", Singbox: customTemplate},
		{Name: "singbox-1.14-custom", Format: "singbox", UA: uaSingbox114, File: "singbox-1.14-custom.json", Singbox: customTemplate},
		{Name: "uri", Format: "uri", File: "uri.txt", Singbox: defaultTemplate},
		{Name: "clash-empty", Format: "clash", Empty: true, File: "clash-empty.yaml", Singbox: defaultTemplate},
		{Name: "clash-premium-empty", Format: "clash-premium", Empty: true, File: "clash-premium-empty.yaml", Singbox: defaultTemplate},
		{Name: "singbox-compat-empty", Format: "singbox", UA: uaSingboxCompat, Empty: true, File: "singbox-compat-empty.json", Singbox: customTemplate},
		{Name: "singbox-1.14-empty", Format: "singbox", UA: uaSingbox114, Empty: true, File: "singbox-1.14-empty.json", Singbox: defaultTemplate},
		{Name: "uri-empty", Format: "uri", Empty: true, File: "uri-empty.txt", Singbox: defaultTemplate},
	}
}

type expiredCase struct {
	expiredJSON
	file string
	body []byte
}

func expiredCases() []expiredCase {
	end := time.Date(2026, 10, 7, 6, 32, 59, 0, time.UTC)
	ext := map[string]string{"clash": "yaml", "clash-premium": "yaml", "singbox": "json", "uri": "txt"}
	mk := func(name, format string, nilLoc bool) expiredCase {
		e := expiredJSON{Name: name, Format: format, PeriodEnd: end.Format(time.RFC3339), NilLocation: nilLoc}
		var loc *time.Location
		if !nilLoc {
			e.TZName, e.TZOffsetSeconds = "CST", 8*3600
			loc = time.FixedZone(e.TZName, e.TZOffsetSeconds)
		}
		e.Notice = subscription.ExpiredNotice(end, loc)
		body, ct := subscription.RenderExpired(subscription.Format(format), e.Notice)
		e.File, e.ContentType = name+"."+ext[format], ct
		return expiredCase{expiredJSON: e, file: e.File, body: body}
	}
	return []expiredCase{
		mk("expired-clash", "clash", false),
		mk("expired-clash-premium", "clash-premium", false),
		mk("expired-singbox", "singbox", false),
		mk("expired-uri", "uri", false),
		mk("expired-uri-utc", "uri", true),
	}
}

// profileCases：前一组记 ProfileName 及其 Content-Disposition；后一组 profile_name 为空，
// 只记 ContentDisposition(site)（读取方遇到空 profile_name 就只比这个）。
func profileCases() []profileJSON {
	var out []profileJSON
	for _, c := range [][3]string{
		{"潘多拉", "妈妈的 iPad", "基础版"},
		{"潘多拉", "", "基础版"},
		{"潘多拉", "  ", " 基础版 "},
		{"潘多拉", "", ""},
		{"", "Home", "Basic"},
		{"Pandora", "Mom iPad", "Basic"},
		{"潘多拉 Cloud", "", ""},
		{"全中文", "", ""},
		{"a\"b;\r\nSet-Cookie:x", "", ""},
		{"", "", ""},
		{" Pandora ", "Ünïcödé", ""},
	} {
		name := subscription.ProfileName(c[0], c[1], c[2])
		out = append(out, profileJSON{Site: c[0], Label: c[1], Plan: c[2], ProfileName: name, ContentDisposition: subscription.ContentDisposition(name)})
	}
	for _, s := range []string{"Pandora", "潘多拉 Cloud", "全中文", "a\"b;\r\nSet-Cookie:x", "", "  ", "a  ·  b"} {
		out = append(out, profileJSON{Site: s, ContentDisposition: subscription.ContentDisposition(s)})
	}
	return out
}

// formatCases：UA 表覆盖旧 DetectFormat / clashFlavor / UAFamily / singboxDialectFor 的每个分支，
// 与每个显式参数取值交叉。
func formatCases() []formatJSON {
	uas := []string{
		"", "curl/8.14.1", "Mozilla/5.0 (Windows NT 10.0)", "某个还没出现的客户端/1.0",
		"sing-box 1.10.0", "SFI/1.9.0 (iOS)", "SFA/1.9.0 (Android)", "SFM/1.9.0", "SFT/1.10.0", "HiddifyNext/2.0.5", "Karing/1.0.0", "Hiddify/2.5 (sing-box)",
		"Shadowrocket/2.2.28", "Quantumult%20X/1.0.30", "Surge/5.8.0", "Loon/3.1.3", "v2rayN/6.23", "v2rayNG/1.8.19", "V2rayU/4.0", "Qv2ray/2.7", "NekoBox/1.3.4", "Nekoray/3.26", "Matsuri/0.9", "SagerNet/0.8", "Streisand/1.6.4", "Potatso/2.0", "OneClick/1.0", "Shadowsocks/1.0", "v2ray-core/5.0", "Clash-Shadowrocket/1.0",
		"clash-verge/v1.7.7", "mihomo/1.19.10", "Stash/2.5.0", "FlClash/0.8.60", "clash-nyanpasu/1.5.1", "ClashMetaForAndroid/2.11.5.Meta", "ClashX Meta/1.4.0", "Clash.Meta", "mihomo.party/v1.5.0", "OpenClash", "Koala Clash/1.0", "ClashMi/1.0", "meta-client/1.0", "nyanpasu/1.0",
		"ClashforWindows/0.20.39", "ClashX/1.118.0", "ClashX Pro/1.118.0", "ClashForAndroid/2.5.12", "clash",
	}
	explicits := []string{
		"", "clash", "meta", "clashmeta", "clash-meta", "mihomo", "verge", "stash", "clash-premium", "premium",
		"singbox", "sing-box", "singbox-json", "hiddify", "karing",
		"uri", "base64", "v2ray", "v2rayn", "v2rayng", "shadowrocket", "general", "  Clash  ", "什么格式",
	}
	out := make([]formatJSON, 0, len(uas)*len(explicits))
	for _, ua := range uas {
		for _, ex := range explicits {
			out = append(out, formatJSON{
				UA: ua, Explicit: ex,
				Format: string(subscription.DetectFormat(ua, ex)),
				Family: subscription.UAFamily(ua),
			})
		}
	}
	return out
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("%s：%v", path, err)
	}
	writeFile(path, append(b, '\n'))
}

func writeFile(path string, b []byte) {
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Fatal(err)
	}
}
