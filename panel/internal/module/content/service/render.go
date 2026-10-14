package service

import (
	"encoding/base64"
	"net/url"
	"regexp"
	"strings"
)

var (
	accessStartRe = regexp.MustCompile(`<!--\s*access\s+start\s*-->`)
	accessEndRe   = regexp.MustCompile(`<!--\s*access\s+end\s*-->`)
)

// 占位变量（与 Xboard 同名，外加 siteUrl）：
//   {{siteName}} 站点名；{{siteUrl}} 面板对外地址；{{subscribeUrl}} 订阅链接；
//   {{urlEncodeSubscribeUrl}} 订阅链接做 URL 编码（url.QueryEscape，同 PHP urlencode）；
//   {{safeBase64SubscribeUrl}} 订阅链接做 URL 安全的 base64、去掉 =（base64.RawURLEncoding）。
// 区块：<!--access start--> … <!--access end-->（Xboard 语法，标记内允许多余空白，如 <!-- access start -->）。
type RenderVars struct {
	SiteName     string
	SiteURL      string
	SubscribeURL string // 没有生效订阅时为空串
}

// AccessDeniedNotice 是没有有效订阅时区块被换成的提示。
const AccessDeniedNotice = "（这部分内容需要有效订阅才能查看）"

// NeedsSubscribeURL 报告正文里是否用到了订阅链接类变量（三个 *ubscribeUrl}} 变量任一），用来决定要不要去取链接。
func NeedsSubscribeURL(body string) bool {
	return strings.Contains(body, "{{subscribeUrl}}") ||
		strings.Contains(body, "{{urlEncodeSubscribeUrl}}") ||
		strings.Contains(body, "{{safeBase64SubscribeUrl}}")
}

// RenderBody 先处理区块、再替换占位变量：
//   hasAccess 为 true：去掉区块的起止标记，保留区块内容；
//   hasAccess 为 false：整个区块（含标记与内容）换成 AccessDeniedNotice；
//   只有起始没有结束标记的：从起始标记到正文末尾都算区块（宁可多藏，不漏）；
//   落单的结束标记一律删掉。
// 占位变量只替换上面五个，其余 {{…}} 原样保留。
func RenderBody(body string, v RenderVars, hasAccess bool) string {
	body = processAccessBlocks(body, hasAccess)
	enc := url.QueryEscape(v.SubscribeURL)
	b64 := base64.RawURLEncoding.EncodeToString([]byte(v.SubscribeURL))
	repl := strings.NewReplacer(
		"{{siteName}}", v.SiteName,
		"{{siteUrl}}", v.SiteURL,
		"{{subscribeUrl}}", v.SubscribeURL,
		"{{urlEncodeSubscribeUrl}}", enc,
		"{{safeBase64SubscribeUrl}}", b64,
	)
	return repl.Replace(body)
}

func processAccessBlocks(body string, hasAccess bool) string {
	for {
		start := accessStartRe.FindStringIndex(body)
		if start == nil {
			break
		}
		rest := body[start[1]:]
		end := accessEndRe.FindStringIndex(rest)
		if end == nil {
			if hasAccess {
				inner := rest
				body = body[:start[0]] + inner
			} else {
				body = body[:start[0]] + AccessDeniedNotice
			}
			break
		}
		inner := rest[:end[0]]
		after := rest[end[1]:]
		if hasAccess {
			body = body[:start[0]] + inner + after
		} else {
			body = body[:start[0]] + AccessDeniedNotice + after
		}
	}
	body = accessEndRe.ReplaceAllString(body, "")
	return body
}
