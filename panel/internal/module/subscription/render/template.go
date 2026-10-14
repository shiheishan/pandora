package render

// 后台可编辑的订阅模板——哪些格式有、占位符、内置缺省、校验与预览；模板原文由 subscription 模块存库，这里只管文本。

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// MaxTemplateBytes 是一份模板的上限（与迁移 00039 的 CHECK 一致）。
const MaxTemplateBytes = 256 << 10

// TemplateFormats 是有可编辑模板的格式，顺序即后台列表顺序。
var TemplateFormats = []Format{
	FormatClash,
	FormatClashPremium,
	FormatStash,
	FormatSingbox,
	FormatSurfboard,
	FormatLoon,
}

// Templatable 判断 f 有没有可编辑模板。
func Templatable(f Format) bool {
	for _, candidate := range TemplateFormats {
		if f == candidate {
			return true
		}
	}
	return false
}

// Placeholder 是模板里的一个占位符。
type Placeholder struct {
	Token    string // 原样写进模板的文本，如 {{proxies}}；sing-box 的带引号，如 "{{outbounds}}"
	Required bool
	Desc     string // 中文说明
}

// TemplatePlaceholders 是 f 的模板可用的占位符（不可编辑的格式返回 nil）。
func TemplatePlaceholders(f Format) []Placeholder {
	switch f {
	case FormatClash, FormatClashPremium, FormatStash:
		return []Placeholder{
			{Token: phProxies, Required: true, Desc: "整份 proxies 段（单独一行顶格）"},
			{Token: phProxyGroups, Required: true, Desc: "自动选择与节点选择两个组（单独一行顶格）"},
			{Token: phProxyNames, Required: false, Desc: "节点名列表（行内，给自定义组用，没有节点时是 DIRECT）"},
		}
	case FormatSurfboard:
		return []Placeholder{
			{Token: phProxies, Required: true, Desc: "全部节点（单独一行）"},
			{Token: phProxyGroups, Required: true, Desc: "策略组（单独一行）"},
			{Token: phProxyNames, Required: false, Desc: "节点名列表（行内）"},
			{Token: phSubscribeURL, Required: false, Desc: "这份订阅的链接，写在 #!MANAGED-CONFIG 行"},
		}
	case FormatLoon:
		return []Placeholder{
			{Token: phProxies, Required: true, Desc: "全部节点（单独一行）"},
		}
	case FormatSingbox:
		return []Placeholder{
			{Token: phOutbounds, Required: true, Desc: "全部出站，含 节点选择、自动选择、direct（写在 outbounds 数组里，连引号）"},
			{Token: phProxyTags, Required: false, Desc: "节点的 tag 列表（写在某个数组里，连引号）"},
		}
	default:
		return nil
	}
}

// DefaultTemplate 是 f 的内置模板原文；不可编辑的格式返回空串。sing-box 的按 opt 里的规则集前缀与拦广告生成。
func DefaultTemplate(f Format, opt Options) string {
	switch f {
	case FormatClash, FormatClashPremium, FormatStash:
		return defaultClashTemplate
	case FormatSurfboard:
		return defaultSurfboardTemplate
	case FormatLoon:
		return defaultLoonTemplate
	case FormatSingbox:
		return singboxDefaultTemplate(opt)
	default:
		return ""
	}
}

// TemplateError 是模板校验不过的原因（中文，可直接给站长看）。
type TemplateError struct{ Reason string }

func (e *TemplateError) Error() string { return e.Reason }

const (
	sampleUUID         = "00000000-0000-4000-8000-000000000000"
	sampleSubscribeURL = "https://sub.example.com/000000000000/preview"
)

// sampleNodes 返回校验与预览用的四条虚构示例节点。
func sampleNodes() []Node {
	return []Node{
		{
			Name: "示例 · 香港 01",
			Type: "shadowsocks",
			Host: "hk.example.com",
			Port: 8388,
			Config: map[string]any{
				"method": "aes-128-gcm",
			},
		},
		{
			Name: "示例 · 日本 01",
			Type: "vmess",
			Host: "jp.example.com",
			Port: 443,
			Config: map[string]any{
				"network":     "ws",
				"tls":         true,
				"server_name": "jp.example.com",
				"ws_path":     "/ws",
			},
		},
		{
			Name: "示例 · 美国 01",
			Type: "trojan",
			Host: "us.example.com",
			Port: 443,
			Config: map[string]any{
				"server_name": "us.example.com",
			},
		},
		{
			Name: "示例 · 新加坡 01",
			Type: "hysteria2",
			Host: "sg.example.com",
			Port: 443,
			Config: map[string]any{
				"server_name": "sg.example.com",
			},
		},
	}
}

// ValidateTemplate 校验一份模板；不过时返回 *TemplateError。
func ValidateTemplate(f Format, content string) error {
	// 1. 基本约束
	if !Templatable(f) {
		return &TemplateError{Reason: "这种格式没有模板"}
	}
	if strings.TrimSpace(content) == "" {
		return &TemplateError{Reason: "模板不能为空"}
	}
	if len(content) > MaxTemplateBytes {
		return &TemplateError{Reason: "模板不能超过 256 KB"}
	}
	if !utf8.ValidString(content) {
		return &TemplateError{Reason: "模板不是有效的 UTF-8 文本"}
	}
	if strings.ContainsRune(content, 0) {
		return &TemplateError{Reason: "模板里不能有空字符"}
	}

	// 2. 必需占位符
	lines := strings.Split(content, "\n")
	hasExactLine := func(token string) bool {
		for _, line := range lines {
			if strings.TrimSuffix(line, "\r") == token {
				return true
			}
		}
		return false
	}

	// 必需占位只能出现一次：多出来的一处（哪怕写在注释里）也会被替换，
	// 块级占位会把节点行挂进别的列表，sing-box 的出站会重复 tag
	once := func(tok string) *TemplateError {
		switch n := strings.Count(content, tok); {
		case n == 0:
			return &TemplateError{Reason: "缺少占位 " + tok}
		case n > 1:
			return &TemplateError{Reason: tok + " 只能出现一次"}
		}
		return nil
	}
	var block []string
	switch f {
	case FormatClash, FormatClashPremium, FormatStash, FormatSurfboard:
		block = []string{phProxies, phProxyGroups}
	case FormatLoon:
		block = []string{phProxies}
	case FormatSingbox:
		if te := once(phOutbounds); te != nil {
			return te
		}
	}
	for _, tok := range block {
		if te := once(tok); te != nil {
			return te
		}
		if !hasExactLine(tok) {
			return &TemplateError{Reason: tok + " 要单独占一行、顶格写"}
		}
	}

	// 3. 试渲染
	switch f {
	case FormatClash, FormatClashPremium, FormatStash:
		for _, nodes := range [][]Node{sampleNodes(), nil} {
			body, _, _ := RenderRequest(Request{
				Format:       f,
				Nodes:        nodes,
				ProxyUUID:    sampleUUID,
				Template:     content,
				SubscribeURL: sampleSubscribeURL,
			})
			var m map[string]any
			if err := yaml.Unmarshal(body, &m); err != nil {
				return &TemplateError{Reason: "YAML 解析失败：" + err.Error()}
			}
			_, ok1 := m["proxies"].([]any)
			pgs, ok2 := m["proxy-groups"].([]any)
			if !ok1 || !ok2 || len(pgs) == 0 {
				return &TemplateError{Reason: "proxies 与 proxy-groups 必须是列表"}
			}
		}
	case FormatSingbox:
		for _, nodes := range [][]Node{sampleNodes(), nil} {
			body, _, _ := RenderRequest(Request{
				Format:       f,
				Nodes:        nodes,
				ProxyUUID:    sampleUUID,
				Template:     content,
				SubscribeURL: sampleSubscribeURL,
			})
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				return &TemplateError{Reason: "JSON 解析失败：" + err.Error()}
			}
			if _, ok := m["outbounds"].([]any); !ok {
				return &TemplateError{Reason: "outbounds 必须是数组"}
			}
		}
	case FormatSurfboard:
		hasProxy, hasProxyGroup := false, false
		for _, line := range lines {
			switch strings.TrimSpace(line) {
			case "[Proxy]":
				hasProxy = true
			case "[Proxy Group]":
				hasProxyGroup = true
			}
		}
		if !hasProxy {
			return &TemplateError{Reason: "缺少 [Proxy] 段"}
		}
		if !hasProxyGroup {
			return &TemplateError{Reason: "缺少 [Proxy Group] 段"}
		}
	case FormatLoon:
		// 只查第 2 步
	}

	return nil
}

// PreviewTemplate 用内置示例节点按 content 渲染（content 为空即内置模板）。调用方先校验。
func PreviewTemplate(f Format, content string, opt Options) ([]byte, string, int) {
	return RenderRequest(Request{
		Format:       f,
		Nodes:        sampleNodes(),
		ProxyUUID:    sampleUUID,
		Options:      opt,
		Template:     content,
		SubscribeURL: sampleSubscribeURL,
	})
}
