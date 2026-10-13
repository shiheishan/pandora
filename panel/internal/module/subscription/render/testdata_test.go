package render

// testdata 的读取与形状。testdata 由 panel/tools/subgolden 用旧实现生成（P7 随旧代码删除，
// 生成好的文件留在仓库里继续当回归基线）：
//
//	testdata/nodes.json              全部夹具节点，协议配置已是内核形状、经过一次 JSON 往返
//	testdata/golden/manifest.json    每个 golden 文件的生成参数与期望的 Content-Type、节点数
//	testdata/golden/<名>.<ext>       旧实现的输出
//
// 生成命令（在 panel 目录）：go run ./tools/subgolden -out internal/module/subscription/render/testdata

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// 夹具里的固定值，全部虚构（与生成器一致）。
const (
	fixtureUUID       = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"
	fixtureHost       = "node.example.com"
	fixtureRealityPub = "EVmCoU4Swh5-Pv9WHB68iac25hNF2VwABHEnIZOTfyE"
)

// 夹具分组：form 是后台表单能存进库的形状，legacy 是存量里还有、新写入已被拒的形状，
// 其余（extra）是重名、保留名、空名与其它回归用的节点。form 与 legacy 每个都要在渲染矩阵里有一行。
const (
	groupForm   = "form"
	groupLegacy = "legacy"
)

const regenerateHint = "先在 panel 目录运行 go run ./tools/subgolden -out internal/module/subscription/render/testdata 生成"

// fixtureNode 是 nodes.json 的一项。
type fixtureNode struct {
	Group       string         `json:"group"`
	Name        string         `json:"name"`
	Type        string         `json:"type"`
	Host        string         `json:"host"`
	Port        int            `json:"port"`
	TrafficRate float64        `json:"traffic_rate"`
	Config      map[string]any `json:"config"`
}

// goldenTemplate 是一份 sing-box 模板配置。
type goldenTemplate struct {
	GeositeURLPrefix string `json:"geosite_url_prefix"`
	GeoIPURLPrefix   string `json:"geoip_url_prefix"`
	BlockAds         bool   `json:"block_ads"`
}

// goldenRender 是一个 Render 的 golden：Empty 为真时不传任何节点。
type goldenRender struct {
	Name        string         `json:"name"`
	Format      string         `json:"format"`
	UA          string         `json:"ua"`
	Empty       bool           `json:"empty"`
	Singbox     goldenTemplate `json:"singbox"`
	File        string         `json:"file"`
	ContentType string         `json:"content_type"`
	Count       int            `json:"count"`
}

// goldenExpired 是一个过期提示的 golden。NilLocation 为真时 ExpiredNotice 传 nil 时区。
type goldenExpired struct {
	Name            string `json:"name"`
	Format          string `json:"format"`
	PeriodEnd       string `json:"period_end"` // RFC 3339
	TZName          string `json:"tz_name"`
	TZOffsetSeconds int    `json:"tz_offset_seconds"`
	NilLocation     bool   `json:"nil_location"`
	Notice          string `json:"notice"`
	File            string `json:"file"`
	ContentType     string `json:"content_type"`
}

// goldenProfile 是 ProfileName 与 ContentDisposition 的一组输入输出。
type goldenProfile struct {
	Site               string `json:"site"`
	Label              string `json:"label"`
	Plan               string `json:"plan"`
	ProfileName        string `json:"profile_name"`
	ContentDisposition string `json:"content_disposition"`
}

// goldenFormat 是 DetectFormat 与 UAFamily 的一组输入输出。
type goldenFormat struct {
	UA       string `json:"ua"`
	Explicit string `json:"explicit"`
	Format   string `json:"format"`
	Family   string `json:"family"`
}

type goldenManifest struct {
	ProxyUUID string          `json:"proxy_uuid"`
	Renders   []goldenRender  `json:"renders"`
	Expired   []goldenExpired `json:"expired"`
	Profiles  []goldenProfile `json:"profiles"`
	Formats   []goldenFormat  `json:"formats"`
}

// readTestdata 读 testdata 下的文件；不存在时给出怎么生成，而不是 panic。
func readTestdata(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", filepath.FromSlash(rel)))
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("testdata/%s 不存在：%s", rel, regenerateHint)
	}
	if err != nil {
		t.Fatalf("读 testdata/%s：%v", rel, err)
	}
	return b
}

// loadFixtures 读 nodes.json 的全部项（含分组）。
func loadFixtures(t *testing.T) []fixtureNode {
	t.Helper()
	var out []fixtureNode
	if err := json.Unmarshal(readTestdata(t, "nodes.json"), &out); err != nil {
		t.Fatalf("testdata/nodes.json 不是合法 JSON：%v（%s）", err, regenerateHint)
	}
	if len(out) == 0 {
		t.Fatalf("testdata/nodes.json 是空的：%s", regenerateHint)
	}
	return out
}

// toNode 把夹具变成渲染输入。
func (f fixtureNode) toNode() Node {
	return Node{Name: f.Name, Type: f.Type, Host: f.Host, Port: f.Port, Config: f.Config, TrafficRate: f.TrafficRate}
}

// loadNodes 读全部夹具节点（每次调用都重新解码，各测试之间不共享 map）。
func loadNodes(t *testing.T) []Node {
	t.Helper()
	fixtures := loadFixtures(t)
	out := make([]Node, 0, len(fixtures))
	for _, f := range fixtures {
		out = append(out, f.toNode())
	}
	return out
}

// loadGroup 只读某几个分组的夹具节点。
func loadGroup(t *testing.T, groups ...string) []Node {
	t.Helper()
	want := map[string]bool{}
	for _, g := range groups {
		want[g] = true
	}
	var out []Node
	for _, f := range loadFixtures(t) {
		if want[f.Group] {
			out = append(out, f.toNode())
		}
	}
	return out
}

// nodeByName 按名字取一个夹具节点（名字是渲染前的原名）。
func nodeByName(t *testing.T, name string) Node {
	t.Helper()
	for _, f := range loadFixtures(t) {
		if f.Name == name {
			return f.toNode()
		}
	}
	t.Fatalf("testdata/nodes.json 里没有名为 %q 的夹具：%s", name, regenerateHint)
	return Node{}
}

// loadManifest 读 golden/manifest.json。
func loadManifest(t *testing.T) goldenManifest {
	t.Helper()
	var m goldenManifest
	if err := json.Unmarshal(readTestdata(t, "golden/manifest.json"), &m); err != nil {
		t.Fatalf("testdata/golden/manifest.json 不是合法 JSON：%v（%s）", err, regenerateHint)
	}
	return m
}

// options 把 manifest 里的模板配置变成渲染参数。
func (g goldenTemplate) options() Options {
	return Options{Singbox: SingboxTemplate{
		GeositeURLPrefix: g.GeositeURLPrefix,
		GeoIPURLPrefix:   g.GeoIPURLPrefix,
		BlockAds:         g.BlockAds,
	}}
}
