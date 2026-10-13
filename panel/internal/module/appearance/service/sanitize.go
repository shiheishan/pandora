package service

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// 插槽 HTML 的净化。
//
// 这段代码是整个插槽功能的安全命门。插槽内容由管理员填写，渲染在**用户**
// 的浏览器里 —— 一旦有一个管理员账号被盗，或者将来给客服开了外观权限，
// 一段 <script> 就能把所有登录用户的令牌带走。所以这里用白名单：
// 不在名单上的标签和属性一律丢弃，而不是「把已知的坏东西过滤掉」——
// 黑名单永远漏，绕过 XSS 黑名单是一项成熟到有教科书的手艺。
//
// 净化在写入时做一次，库里只存净化后的结果。

// allowedTags 是能保留的标签。
//
// 没有 form / input / button：插槽是用来讲话的，不是用来收集东西的。
// 一个长得像登录框的插槽会是很有效的钓鱼页，而且就长在真站点上。
// 没有 iframe / object / embed：它们能把任意第三方页面嵌进来。
// 没有 style 标签：任意 CSS 是注入面，样式只走主题令牌。
var allowedTags = map[string]bool{
	"p": true, "br": true, "hr": true, "span": true, "div": true,
	"b": true, "strong": true, "i": true, "em": true, "u": true,
	"s": true, "del": true, "mark": true, "small": true, "code": true,
	"pre": true, "blockquote": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "dl": true, "dt": true, "dd": true,
	"table": true, "thead": true, "tbody": true, "tr": true, "th": true, "td": true,
	"a": true, "img": true,
}

// allowedAttrs 按标签列出可保留的属性。
//
// 全局只放 class 和 title：class 让内容能蹭上主题里已有的样式，
// 不必每段都写内联样式。style 属性没放开 —— 它能做定位和覆盖，
// 足以把一个透明层盖在「确认支付」按钮上。
var globalAttrs = map[string]bool{"class": true, "title": true}

var allowedAttrs = map[string]map[string]bool{
	"a":   {"href": true, "target": true, "rel": true},
	"img": {"src": true, "alt": true, "width": true, "height": true, "loading": true},
	"td":  {"colspan": true, "rowspan": true},
	"th":  {"colspan": true, "rowspan": true, "scope": true},
	"ol":  {"start": true},
}

// SanitizeHTML 把一段管理员写的 HTML 洗成可以安全插进用户端的片段。
//
// 返回净化后的 HTML 和被丢弃的东西的说明 —— 后者会回给管理员，
// 否则「我明明写了按钮怎么没了」这种问题只能靠猜。
func SanitizeHTML(in string) (string, []string) {
	in = strings.TrimSpace(in)
	if in == "" {
		return "", nil
	}

	// 上下文节点必须带上 DataAtom：ParseFragment 是按 atom 而不是按
	// Data 字符串来决定插入模式的，留 0 会让它直接报错，
	// 于是每一段输入都走进下面那条「整段丢弃」——功能全废而且静悄悄。
	nodes, err := html.ParseFragment(strings.NewReader(in), &html.Node{
		Type: html.ElementNode, Data: "div", DataAtom: atom.Div,
	})
	if err != nil {
		// 解析器几乎不会失败（HTML 规范要求容错），真失败了就当整段无效，
		// 而不是把没解析成功的原文放行。
		return "", []string{"内容无法解析为 HTML，已整段丢弃"}
	}

	dropped := map[string]bool{}
	var out strings.Builder
	for _, n := range nodes {
		writeNode(&out, n, dropped, 0)
	}

	notes := make([]string, 0, len(dropped))
	for d := range dropped {
		notes = append(notes, d)
	}
	sort.Strings(notes)
	return strings.TrimSpace(out.String()), notes
}

// maxDepth 限制嵌套深度。
//
// 一段一万层深的 <div> 不会触发上面任何一条规则，但会让浏览器渲染时
// 明显卡顿，也会让我们自己的递归吃掉栈。
const maxDepth = 20

func writeNode(out *strings.Builder, n *html.Node, dropped map[string]bool, depth int) {
	if depth > maxDepth {
		dropped["嵌套层级超过 20 层的部分已丢弃"] = true
		return
	}

	switch n.Type {
	case html.TextNode:
		out.WriteString(html.EscapeString(n.Data))
		return

	case html.CommentNode, html.DoctypeNode:
		// 注释不渲染，但会原样留在页面源码里。管理员在注释里写的
		// 内部备注不该跟着发给用户。
		return

	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if !allowedTags[tag] {
			dropped[fmt.Sprintf("不允许的标签 <%s>（内部文字已保留）", tag)] = true
			// 标签丢掉但保留里面的文字：管理员写了 <section>一段话</section>
			// 时，把话也一起吞掉比留下更让人困惑。
			// script / style 例外 —— 它们的「文字」就是代码本身。
			if tag == "script" || tag == "style" {
				return
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				writeNode(out, c, dropped, depth+1)
			}
			return
		}

		attrs := cleanAttrs(tag, n.Attr, dropped)

		// src 被拒掉的 <img> 直接丢整个标签。
		// 留着的话页面上会出现一个碎图占位符 —— 管理员看到的是
		// 「我的图没显示」，而真实原因（地址不被允许）藏在别处。
		if tag == "img" && !hasAttr(attrs, "src") {
			dropped["图片地址不被允许，整个 <img> 已丢弃"] = true
			return
		}

		out.WriteString("<" + tag)
		for _, a := range attrs {
			out.WriteString(" " + a.Key + `="` + html.EscapeString(a.Val) + `"`)
		}
		if isVoid(tag) {
			out.WriteString(">")
			return
		}
		out.WriteString(">")
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeNode(out, c, dropped, depth+1)
		}
		out.WriteString("</" + tag + ">")
		return

	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			writeNode(out, c, dropped, depth+1)
		}
	}
}

func cleanAttrs(tag string, attrs []html.Attribute, dropped map[string]bool) []html.Attribute {
	out := make([]html.Attribute, 0, len(attrs))
	for _, a := range attrs {
		key := strings.ToLower(a.Key)

		// on* 单独说一句：这是最直接的一类注入（onclick、onerror、
		// onmouseover…）。它们本来也过不了下面的白名单，这里显式拦一道
		// 是为了给管理员一条说得清的提示。
		if strings.HasPrefix(key, "on") {
			dropped["事件属性（on* ）一律不允许"] = true
			continue
		}
		if key == "style" {
			dropped["style 属性不允许，样式请改用主题令牌"] = true
			continue
		}
		if !globalAttrs[key] && !allowedAttrs[tag][key] {
			dropped[fmt.Sprintf("<%s> 上不允许的属性 %s", tag, key)] = true
			continue
		}

		if key == "href" || key == "src" {
			v, ok := safeURL(a.Val)
			if !ok {
				dropped["只允许 http/https/mailto 链接与 data:image 图片"] = true
				continue
			}
			a.Val = v
		}
		out = append(out, html.Attribute{Key: key, Val: a.Val})
	}

	// 外链一律补 rel：没有 noopener 的 target=_blank 会把 window.opener
	// 交给对方页面，对方可以把我们这一页导航到钓鱼站。
	if tag == "a" {
		hasTargetBlank := false
		for _, a := range out {
			if a.Key == "target" && strings.EqualFold(a.Val, "_blank") {
				hasTargetBlank = true
			}
		}
		if hasTargetBlank {
			filtered := out[:0]
			for _, a := range out {
				if a.Key != "rel" {
					filtered = append(filtered, a)
				}
			}
			out = append(filtered, html.Attribute{Key: "rel", Val: "noopener noreferrer"})
		}
	}
	return out
}

// safeURL 只放行几种协议。
//
// 重点是挡住 javascript: —— 它是 href 里最常见的注入载体。
// 顺带挡 data:（图片除外）：data:text/html 能在同源下执行脚本。
func safeURL(raw string) (string, bool) {
	v := strings.TrimSpace(raw)
	// 去掉控制字符再判协议：\x00 和换行能把 "java\nscript:" 藏过朴素的前缀比较，
	// 而浏览器解析 URL 时会先把它们剔掉。
	v = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, v)
	if v == "" {
		return "", false
	}

	lower := strings.ToLower(v)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"),
		strings.HasPrefix(lower, "mailto:"):
		return v, true
	case strings.HasPrefix(lower, "data:image/png;base64,"),
		strings.HasPrefix(lower, "data:image/jpeg;base64,"),
		strings.HasPrefix(lower, "data:image/gif;base64,"),
		strings.HasPrefix(lower, "data:image/webp;base64,"):
		// 不含 svg：SVG 里可以写 <script>，data:image/svg+xml 是个
		// 伪装成图片的脚本执行入口。
		return v, true
	case strings.HasPrefix(v, "/") && !strings.HasPrefix(v, "//"):
		// 站内相对路径可以。排除 //host 那种协议相对地址，
		// 它其实是外链，只是看起来像路径。
		return v, true
	case strings.HasPrefix(v, "#"):
		return v, true
	}
	return "", false
}

func hasAttr(attrs []html.Attribute, key string) bool {
	for _, a := range attrs {
		if a.Key == key {
			return true
		}
	}
	return false
}

func isVoid(tag string) bool {
	switch tag {
	case "br", "hr", "img":
		return true
	}
	return false
}
