package webapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"html"
	"net/url"
	"strings"
	"unicode"
)

// IndexMeta 是入口页 <head> 里按站点信息改写的部分；零值表示原样下发。
type IndexMeta struct {
	Title       string // 替换 <title> 的文字；空则不动 <title>
	Description string // <meta name="description" content="…" />；空则不写
	AppName     string // <meta name="application-name" content="…" />（前端登录页据此取站点名）；空则不写
	Icon        string // <link rel="icon" href="…" />；只收 data:image/ 开头的值（入口页 CSP 是 img-src 'self' data:），否则不写
	Keywords    string // <meta name="keywords" content="…" />；空则不写
	Robots      string // <meta name="robots" content="…" />；空则不写
	URL         string // <link rel="canonical" href="…" /> 与 og:url；只收 http(s) 且 Host 非空；否则不写
	OpenGraph   bool   // 为 true 时写 Open Graph 标签（og:type 总写，其余项有值才写）
}

func (m IndexMeta) zero() bool {
	return m.Title == "" && m.Description == "" && m.AppName == "" && m.Icon == "" &&
		m.Keywords == "" && m.Robots == "" && m.URL == "" && !m.OpenGraph
}

// WithIndexMeta 让入口页每次请求时调 f 取站点信息改写 <head>。f 要便宜（入口页每次回源校验），缓存由 f 自己做。
// f 返回零值时入口与不带本选项时逐字一致（ETag 也一致）。
func WithIndexMeta(f func(ctx context.Context) IndexMeta) Option {
	return func(h *handler) { h.meta = f }
}

func sanitizeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func sanitizeIcon(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "data:image/") {
		return ""
	}
	return s
}

func sanitizeURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return s
}

func sanitizeMeta(m IndexMeta) IndexMeta {
	return IndexMeta{
		Title:       sanitizeText(m.Title),
		Description: sanitizeText(m.Description),
		AppName:     sanitizeText(m.AppName),
		Icon:        sanitizeIcon(m.Icon),
		Keywords:    sanitizeText(m.Keywords),
		Robots:      sanitizeText(m.Robots),
		URL:         sanitizeURL(m.URL),
		OpenGraph:   m.OpenGraph,
	}
}

// renderIndex 按站点信息改写入口页 <head>；清洗后 zero 为真则原样返回 index。
func renderIndex(index []byte, m IndexMeta) []byte {
	m = sanitizeMeta(m)
	if m.zero() {
		return index
	}

	s := string(index)
	out := s

	if m.Title != "" {
		escTitle := html.EscapeString(m.Title)
		start := strings.Index(out, "<title>")
		if start >= 0 {
			end := strings.Index(out[start:], "</title>")
			if end >= 0 {
				end += start
				out = out[:start+len("<title>")] + escTitle + out[end:]
			} else {
				out = insertBeforeHeadClose(out, "<title>"+escTitle+"</title>")
			}
		} else {
			out = insertBeforeHeadClose(out, "<title>"+escTitle+"</title>")
		}
	}

	var tags strings.Builder
	if m.Description != "" {
		tags.WriteString("\n    <meta name=\"description\" content=\"" + html.EscapeString(m.Description) + "\" />")
	}
	if m.Keywords != "" {
		tags.WriteString("\n    <meta name=\"keywords\" content=\"" + html.EscapeString(m.Keywords) + "\" />")
	}
	if m.Robots != "" {
		tags.WriteString("\n    <meta name=\"robots\" content=\"" + html.EscapeString(m.Robots) + "\" />")
	}
	if m.AppName != "" {
		tags.WriteString("\n    <meta name=\"application-name\" content=\"" + html.EscapeString(m.AppName) + "\" />")
	}
	if m.Icon != "" {
		tags.WriteString("\n    <link rel=\"icon\" href=\"" + html.EscapeString(m.Icon) + "\" />")
	}
	if m.URL != "" {
		tags.WriteString("\n    <link rel=\"canonical\" href=\"" + html.EscapeString(m.URL) + "\" />")
	}
	if m.OpenGraph {
		tags.WriteString("\n    <meta property=\"og:type\" content=\"website\" />")
		if m.AppName != "" {
			tags.WriteString("\n    <meta property=\"og:site_name\" content=\"" + html.EscapeString(m.AppName) + "\" />")
		}
		if m.Title != "" {
			tags.WriteString("\n    <meta property=\"og:title\" content=\"" + html.EscapeString(m.Title) + "\" />")
		}
		if m.Description != "" {
			tags.WriteString("\n    <meta property=\"og:description\" content=\"" + html.EscapeString(m.Description) + "\" />")
		}
		if m.URL != "" {
			tags.WriteString("\n    <meta property=\"og:url\" content=\"" + html.EscapeString(m.URL) + "\" />")
		}
	}
	if tags.Len() > 0 {
		out = insertBeforeHeadClose(out, tags.String())
	}

	if out == s {
		return index
	}
	return []byte(out)
}

// insertBeforeHeadClose 在第一个 </head> 前插入片段；没有 </head> 时原样返回。
// </head> 前若是「换行 + 缩进」，片段插在那个换行之前，</head> 仍独占一行。
func insertBeforeHeadClose(doc, insert string) string {
	idx := strings.Index(doc, "</head>")
	if idx < 0 {
		return doc
	}
	at := idx
	for at > 0 && (doc[at-1] == ' ' || doc[at-1] == '\t') {
		at--
	}
	if at == 0 || doc[at-1] != '\n' {
		at = idx
	} else {
		at--
	}
	return doc[:at] + insert + doc[at:]
}

// rendered 返回按 m 改写后的入口与 ETag；meta 不变时复用上次的结果（入口页是最热的地址，不每次重写、重算哈希）。
func (h *handler) rendered(m IndexMeta) ([]byte, string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hasLast && h.lastMeta == m {
		return h.lastBody, h.lastETag
	}
	body := renderIndex(h.index, m)
	sum := sha256.Sum256(body)
	h.lastMeta, h.lastBody, h.lastETag, h.hasLast = m, body, `"`+hex.EncodeToString(sum[:8])+`"`, true
	return h.lastBody, h.lastETag
}
