// 门户首页片段：入口页 <body> 里 #root 之前由服务端输出的首页内容；
// 只用 html/template 自动转义，不出 <script>、<style>、style= 与事件属性（入口页 CSP 是 script-src 'self'、style-src 'self'，没有 nonce / hash），
// 样式全在门户前端的全局样式 portal/home.css 里，类名统一 ph- 前缀。
package webapp

import (
	"bytes"
	"context"
	"html/template"
	"slices"
	"strings"
	"unicode"
)

// Home 是门户首页片段的内容，由装配处组好（本包不读设置、不懂套餐）；SiteName 为空时不出片段。
type Home struct {
	SiteName string     // h1 与页头品牌
	Tagline  string     // 站点名下的一句话；可空
	Logo     string     // 页头图标；只收 data:image/ 开头的值（入口页 CSP 是 img-src 'self' data:），否则不出图
	Intro    string     // 介绍，纯文本，按换行分段；可空
	Plans    []HomePlan // 套餐卡；空则不出这一节
	FAQ      []HomeFAQ  // 常见问题；空则不出这一节
}

// HomePlan 是一张套餐卡；文字都由装配处算好（价格串、用量串）。
type HomePlan struct {
	Name      string // 套餐名
	Kind      string // 「按时间」「一次性」
	Badge     string // 「推荐」；空则不出
	Desc      string // 套餐描述；可空
	Usage     string // 主要用量，如「每期 200G · 3 台设备」
	Price     string // 主价签，如「¥12.00 / 月」
	PriceNote string // 价签下的小字，如「季付 ¥33.00 · 年付 ¥108.00」「有效 30 天」；可空
}

// HomeFAQ 是常见问题的一条；A 按换行分段。
type HomeFAQ struct {
	Q string
	A string
}

// WithHome 让门户入口页每次请求时调 f 取首页内容，插在 <div id="root"> 之前。f 要便宜（入口页每次回源校验），缓存由 f 自己做；
// f 返回 false（或 SiteName 清洗后为空）时入口与不带本选项时逐字一致（ETag 也一致）。
func WithHome(f func(ctx context.Context) (Home, bool)) Option {
	return func(h *handler) { h.home = f }
}

func (h Home) equal(o Home) bool {
	return h.SiteName == o.SiteName && h.Tagline == o.Tagline && h.Logo == o.Logo && h.Intro == o.Intro &&
		slices.Equal(h.Plans, o.Plans) && slices.Equal(h.FAQ, o.FAQ)
}

func (h Home) clone() Home {
	out := h
	out.Plans = slices.Clone(h.Plans)
	out.FAQ = slices.Clone(h.FAQ)
	return out
}

func sanitizeMultiline(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

func sanitizeHome(h Home) Home {
	out := Home{
		SiteName: sanitizeText(h.SiteName),
		Tagline:  sanitizeText(h.Tagline),
		Logo:     sanitizeIcon(h.Logo),
		Intro:    sanitizeMultiline(h.Intro),
	}
	for _, p := range h.Plans {
		plan := HomePlan{
			Name:      sanitizeText(p.Name),
			Kind:      sanitizeText(p.Kind),
			Badge:     sanitizeText(p.Badge),
			Desc:      strings.Join(strings.Fields(sanitizeMultiline(p.Desc)), " "), // 多行描述按空格接上，不粘连
			Usage:     sanitizeText(p.Usage),
			Price:     sanitizeText(p.Price),
			PriceNote: sanitizeText(p.PriceNote),
		}
		if plan.Name == "" {
			continue
		}
		out.Plans = append(out.Plans, plan)
	}
	for _, f := range h.FAQ {
		q := sanitizeText(f.Q)
		a := sanitizeMultiline(f.A)
		if q == "" || a == "" {
			continue
		}
		out.FAQ = append(out.FAQ, HomeFAQ{Q: q, A: a})
	}
	return out
}

func paragraphs(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, "\n") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

type homeView struct {
	SiteName string
	Tagline  string
	Logo     template.URL
	Intro    []string
	Plans    []HomePlan
	FAQ      []homeFAQView
}

type homeFAQView struct {
	Q string
	A []string
}

const homeHTML = `<div id="ph-home" class="ph">
  <header class="ph-top">
    <a class="ph-brand" href="./">{{if .Logo}}<img class="ph-logo" src="{{.Logo}}" alt="" width="28" height="28">{{end}}<span class="ph-brand-name">{{.SiteName}}</span></a>
    <nav class="ph-nav" aria-label="账号">
      <a class="ph-link" href="#/login">登录</a>
      <a class="ph-btn ph-btn-sm" href="#/register">注册</a>
    </nav>
  </header>
  <main class="ph-main">
    <section class="ph-hero" aria-labelledby="ph-title">
      <h1 id="ph-title" class="ph-title">{{.SiteName}}</h1>
      {{if .Tagline}}<p class="ph-tagline">{{.Tagline}}</p>{{end}}
      {{range .Intro}}<p class="ph-intro">{{.}}</p>{{end}}
      <p class="ph-cta"><a class="ph-btn" href="#/register">注册</a><a class="ph-btn ph-btn-soft" href="#/login">登录</a></p>
    </section>
    {{if .Plans}}<section class="ph-section" aria-labelledby="ph-plans-h">
      <h2 id="ph-plans-h" class="ph-h2">套餐与价格</h2>
      <ul class="ph-plans">{{range .Plans}}
        <li class="ph-plan{{if .Badge}} ph-plan-rec{{end}}">
          <article class="ph-card">
            <p class="ph-plan-meta"><span class="ph-kind">{{.Kind}}</span>{{if .Badge}}<span class="ph-badge">{{.Badge}}</span>{{end}}</p>
            <h3 class="ph-plan-name">{{.Name}}</h3>
            {{if .Desc}}<p class="ph-desc">{{.Desc}}</p>{{end}}
            {{if .Usage}}<p class="ph-usage">{{.Usage}}</p>{{end}}
            <p class="ph-price">{{.Price}}</p>
            {{if .PriceNote}}<p class="ph-price-note">{{.PriceNote}}</p>{{end}}
            <a class="ph-btn ph-btn-block" href="#/register" aria-label="购买{{.Name}}">购买</a>
          </article>
        </li>{{end}}
      </ul>
    </section>{{end}}
    {{if .FAQ}}<section class="ph-section" aria-labelledby="ph-faq-h">
      <h2 id="ph-faq-h" class="ph-h2">常见问题</h2>
      <div class="ph-faq">{{range .FAQ}}
        <details class="ph-qa"><summary class="ph-q">{{.Q}}</summary><div class="ph-a">{{range .A}}<p>{{.}}</p>{{end}}</div></details>{{end}}
      </div>
    </section>{{end}}
  </main>
  <footer class="ph-foot">
    <p class="ph-copy">© {{.SiteName}}</p>
    <p class="ph-foot-links"><a class="ph-link" href="#/login">登录</a><a class="ph-link" href="#/register">注册</a></p>
  </footer>
</div>`

var homeTmpl = template.Must(template.New("home").Parse(homeHTML))

func renderHome(h Home) string {
	v := homeView{
		SiteName: h.SiteName,
		Tagline:  h.Tagline,
		Logo:     template.URL(""),
		Intro:    paragraphs(h.Intro),
		Plans:    h.Plans,
	}
	if h.Logo != "" {
		v.Logo = template.URL(h.Logo)
	}
	for _, f := range h.FAQ {
		v.FAQ = append(v.FAQ, homeFAQView{Q: f.Q, A: paragraphs(f.A)})
	}
	var buf bytes.Buffer
	if err := homeTmpl.Execute(&buf, v); err != nil {
		return ""
	}
	return buf.String()
}

func insertBeforeRoot(doc []byte, frag string) []byte {
	marker := "<div id=\"root\">"
	s := string(doc)
	idx := strings.Index(s, marker)
	if idx < 0 {
		return doc
	}
	insert := frag + "\n    "
	out := s[:idx] + insert + s[idx:]
	return []byte(out)
}
