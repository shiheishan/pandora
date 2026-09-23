package appearance

import (
	"strings"
	"testing"
)

// 这些用例是按「攻击者会怎么写」来挑的，不是按「功能应该怎么用」。
// 白名单净化的价值全在于挡住没想到的输入，所以这里的重点是变形写法：
// 大小写混写、属性里塞控制字符、用注释切断关键字、靠标签补全绕过。
func TestSanitizeHTML_挡住脚本注入(t *testing.T) {
	attacks := []struct {
		name string
		in   string
		// 净化结果里绝对不能出现的片段（小写比较）
		forbid []string
	}{
		{"直接的 script", `<p>hi</p><script>alert(1)</script>`,
			[]string{"<script", "alert(1)"}},
		{"大小写混写", `<ScRiPt>alert(1)</sCrIpT>`,
			[]string{"<script", "alert(1)"}},
		{"img onerror", `<img src=x onerror="alert(1)">`,
			[]string{"onerror", "alert"}},
		{"事件属性无引号", `<div onclick=alert(1)>x</div>`,
			[]string{"onclick", "alert"}},
		{"javascript 链接", `<a href="javascript:alert(1)">点我</a>`,
			[]string{"javascript:"}},
		{"javascript 里夹换行", "<a href=\"java\nscript:alert(1)\">x</a>",
			[]string{"javascript:", "script:"}},
		{"javascript 里夹 NUL", "<a href=\"java\x00script:alert(1)\">x</a>",
			[]string{"javascript:"}},
		{"data:text/html", `<a href="data:text/html;base64,PHNjcmlwdD4=">x</a>`,
			[]string{"data:text/html"}},
		{"svg 伪装成图片", `<img src="data:image/svg+xml;base64,PHN2Zz4=">`,
			[]string{"svg+xml"}},
		{"iframe 嵌外站", `<iframe src="https://evil.example"></iframe>`,
			[]string{"<iframe"}},
		{"表单钓鱼", `<form action="https://evil.example"><input name="password"></form>`,
			[]string{"<form", "<input"}},
		{"style 属性做覆盖层", `<div style="position:fixed;top:0;left:0;width:100%;height:100%">x</div>`,
			[]string{"position:fixed", "style="}},
		{"style 标签", `<style>body{display:none}</style>`,
			[]string{"<style", "display:none"}},
		{"注释里藏东西", `<!-- <script>alert(1)</script> -->`,
			[]string{"<script", "alert(1)"}},
		{"meta 刷新跳转", `<meta http-equiv="refresh" content="0;url=https://evil.example">`,
			[]string{"<meta", "http-equiv"}},
		{"object 标签", `<object data="https://evil.example"></object>`,
			[]string{"<object"}},
		{"协议相对地址", `<a href="//evil.example/x">x</a>`,
			[]string{"//evil.example"}},
		{"srcset 绕过", `<img src="/ok.png" srcset="https://evil.example/x.png">`,
			[]string{"srcset"}},
		{"formaction", `<button formaction="javascript:alert(1)">x</button>`,
			[]string{"formaction", "javascript:"}},
	}

	for _, tc := range attacks {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := SanitizeHTML(tc.in)
			low := strings.ToLower(out)
			for _, bad := range tc.forbid {
				if strings.Contains(low, strings.ToLower(bad)) {
					t.Errorf("净化后仍含 %q\n输入: %s\n输出: %s", bad, tc.in, out)
				}
			}
		})
	}
}

func TestSanitizeHTML_保留正常内容(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		expect []string // 必须出现
	}{
		{"基本排版", `<p>本站<strong>不接受</strong>退款，请提交工单。</p>`,
			[]string{"<p>", "<strong>", "不接受", "工单"}},
		{"外链带 rel", `<a href="https://example.com" target="_blank">文档</a>`,
			[]string{`href="https://example.com"`, `rel="noopener noreferrer"`}},
		{"站内相对链接", `<a href="/help">帮助</a>`, []string{`href="/help"`}},
		{"class 保留", `<div class="tip">提示</div>`, []string{`class="tip"`}},
		{"列表与表格", `<ul><li>一</li></ul><table><tr><td colspan="2">格</td></tr></table>`,
			[]string{"<ul>", "<li>", "<td", `colspan="2"`}},
		{"未知标签保留文字", `<section>这段话要留着</section>`, []string{"这段话要留着"}},
		{"内嵌 base64 图", `<img src="data:image/png;base64,iVBORw0KGgo=" alt="标">`,
			[]string{"data:image/png;base64", `alt="标"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := SanitizeHTML(tc.in)
			for _, want := range tc.expect {
				if !strings.Contains(out, want) {
					t.Errorf("丢了应保留的内容 %q\n输入: %s\n输出: %s", want, tc.in, out)
				}
			}
		})
	}
}

// 净化过一次的结果再净化一次必须不变。
// 不满足这条就说明净化器自己会产生需要再净化的东西，
// 而库里存的正是净化后的结果 —— 那样每次读都得再洗一遍才安全。
func TestSanitizeHTML_幂等(t *testing.T) {
	inputs := []string{
		`<p>普通<em>文字</em></p>`,
		`<a href="https://x.example" target="_blank">链接</a>`,
		`<img src="data:image/png;base64,iVBORw0KGgo=">`,
		`<div class="a"><ul><li>项</li></ul></div>`,
		`<section><script>alert(1)</script>文字</section>`,
		`<p>&lt;script&gt; 这是文字不是标签</p>`,
	}
	for _, in := range inputs {
		once, _ := SanitizeHTML(in)
		twice, _ := SanitizeHTML(once)
		if once != twice {
			t.Errorf("不幂等\n输入:   %s\n第一次: %s\n第二次: %s", in, once, twice)
		}
	}
}

func TestSanitizeHTML_深度炸弹(t *testing.T) {
	in := strings.Repeat("<div>", 500) + "底" + strings.Repeat("</div>", 500)
	out, notes := SanitizeHTML(in)
	if strings.Count(out, "<div>") > maxDepth+1 {
		t.Errorf("嵌套没有被截断，输出里有 %d 层", strings.Count(out, "<div>"))
	}
	if len(notes) == 0 {
		t.Error("截断了却没有告诉管理员")
	}
}

func TestSanitizeHTML_丢弃说明可读(t *testing.T) {
	_, notes := SanitizeHTML(`<script>x</script><div onclick="y">z</div>`)
	if len(notes) == 0 {
		t.Fatal("丢了东西却没有任何说明，管理员会以为自己写错了")
	}
	joined := strings.Join(notes, " | ")
	if !strings.Contains(joined, "script") || !strings.Contains(joined, "on*") {
		t.Errorf("说明不够具体: %s", joined)
	}
}

func TestSanitizeCSS(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		forbid []string
		expect []string
	}{
		{"外部 import", `@import url("https://evil.example/x.css"); .a{color:red}`,
			[]string{"@import", "evil.example"}, []string{"color:red"}},
		{"外链背景图", `.a{background:url(https://evil.example/track.png)}`,
			[]string{"evil.example"}, []string{"about:blank"}},
		{"expression", `.a{width:expression(alert(1))}`,
			[]string{"expression("}, nil},
		{"闭合 style 标签", `.a{}</style><script>alert(1)</script>`,
			[]string{"</style", "<script"}, nil},
		{"注释里藏 import", `/* @import url(https://evil.example/x) */ .a{color:red}`,
			[]string{"evil.example", "@import"}, []string{"color:red"}},
		{"内嵌图片放行", `.a{background:url(data:image/png;base64,iVBORw0KGgo=)}`,
			nil, []string{"data:image/png;base64"}},
		{"站内图片放行", `.a{background:url(/static/bg.png)}`,
			nil, []string{"/static/bg.png"}},
		{"正常样式原样留下", `.card{border-radius:12px;padding:16px}`,
			nil, []string{"border-radius:12px", "padding:16px"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _ := SanitizeCSS(tc.in)
			low := strings.ToLower(out)
			for _, bad := range tc.forbid {
				if strings.Contains(low, strings.ToLower(bad)) {
					t.Errorf("仍含 %q：%s", bad, out)
				}
			}
			for _, want := range tc.expect {
				if !strings.Contains(out, want) {
					t.Errorf("丢了 %q：%s", want, out)
				}
			}
		})
	}
}

func TestSanitize_空输入(t *testing.T) {
	if out, notes := SanitizeHTML("   "); out != "" || notes != nil {
		t.Errorf("空 HTML 应当原样返回空，得到 %q / %v", out, notes)
	}
	if out, notes := SanitizeCSS("\n\t "); out != "" || notes != nil {
		t.Errorf("空 CSS 应当原样返回空，得到 %q / %v", out, notes)
	}
}

// src 被拒的 img 不该留下一个碎图占位符。
func TestSanitizeHTML_坏图整个丢掉(t *testing.T) {
	for _, in := range []string{
		`<img src=x onerror="alert(1)">`,
		`<img src="javascript:alert(1)">`,
		`<img src="data:image/svg+xml;base64,PHN2Zz4=">`,
		`<img>`,
	} {
		out, _ := SanitizeHTML(in)
		if strings.Contains(out, "<img") {
			t.Errorf("残留空 img\n输入: %s\n输出: %s", in, out)
		}
	}
	// 正常的图还要留着
	out, _ := SanitizeHTML(`<img src="/logo.png" alt="标">`)
	if !strings.Contains(out, `src="/logo.png"`) {
		t.Errorf("正常图片被误删: %s", out)
	}
}
