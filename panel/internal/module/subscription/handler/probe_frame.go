// 订阅域名探测页。为什么是独立页面：门户入口页对匿名访客一样，CSP 不能列订阅域名；这一页只对持票据的本人下发，CSP 按请求只列本人所在组的域名。脚本固定不变、内联，CSP 用它的 sha256 放行；数据（域名、超时）放在 type="application/json" 的数据块里（不执行，不受 script-src 约束），json.Marshal 会把 <、>、& 转义，数据里出不了 </script>。
package handler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// probeFrameScript 是探测页的脚本（固定文本，CSP 按它的 sha256 放行；改动它 hash 自动跟着变）。
const probeFrameScript = `(function () {
  'use strict';
  var data = JSON.parse(document.getElementById('probe-data').textContent);
  var ticket = new URLSearchParams(location.search).get('t') || '';
  var blocked = new Set();
  document.addEventListener('securitypolicyviolation', function (e) {
    try { blocked.add(new URL(e.blockedURI).origin); } catch (_) { /* 非 URL 的不管 */ }
  });
  function done() {
    try { parent.postMessage({ type: 'pandora-sub-probe-done' }, location.origin); } catch (_) { /* 没有父页面 */ }
  }
  function probe(base) {
    var ctl = new AbortController();
    var timer = setTimeout(function () { ctl.abort(); }, data.timeout_ms);
    return fetch(base + '/v1/sub-probe', {
      mode: 'cors', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer', signal: ctl.signal
    }).then(function (res) {
      clearTimeout(timer);
      if (res.status === 204) return 'ok';
      if (res.status === 429 || res.status >= 500) return 'skip';
      return 'fail';
    }, function () {
      clearTimeout(timer);
      return new Promise(function (r) { setTimeout(r, 0); }).then(function () {
        return blocked.has(base) ? 'skip' : 'fail';
      });
    });
  }
  if (!ticket || !data.domains.length || !data.portal_origin || location.origin !== data.portal_origin) { done(); return; }
  Promise.all(data.domains.map(probe)).then(function (outs) {
    var results = [];
    outs.forEach(function (o, i) { if (o !== 'skip') results.push({ base_url: data.domains[i], ok: o === 'ok' }); });
    if (!results.length) return null;
    return fetch(new URL('report', location.href).toString(), {
      method: 'POST', credentials: 'omit', cache: 'no-store',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ticket: ticket, results: results })
    });
  }).catch(function () { return null; }).then(done);
})();`

// probeFrameScriptSrc 是 CSP 里放行 probeFrameScript 的 'sha256-…' 来源。
var probeFrameScriptSrc = func() string {
	sum := sha256.Sum256([]byte(probeFrameScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// probeOriginRE 是放进 CSP 的订阅域名的形状（库里 CHECK 与 NormalizeBaseURL 已保证，这里再挡一次，不合的丢掉）。
var probeOriginRE = regexp.MustCompile(`^https://[a-z0-9.-]+$`)

// probeFrameTimeoutMS 是每个域名探测的超时（毫秒）。
const probeFrameTimeoutMS = 5000

type probeFrameData struct {
	Domains      []string `json:"domains"`
	TimeoutMS    int      `json:"timeout_ms"`
	PortalOrigin string   `json:"portal_origin"`
}

// probeFrameCSP 生成探测页的 CSP：脚本只放行固定脚本的 hash；connect-src 只有同源（上报）与本组域名；只许同源页面嵌入。
func probeFrameCSP(domains []string) string {
	return "default-src 'none'; script-src " + probeFrameScriptSrc +
		"; connect-src 'self' " + strings.Join(domains, " ") +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'self'"
}

// writeProbeFrame 写探测页：先过滤域名形状（全被滤掉时仍回页面，脚本会因为没有域名直接结束）；
// 门户源（portalOrigin）为空时不列任何域名，脚本拿到的门户源与自己的 location.origin 不一致时也不探不报
// （public_base_url 配错或经别的主机名访问时，CORS 只认门户源，探出来全是失败，报上去就是冤枉域名）。
// 去掉全局的 X-Frame-Options（门户要同源嵌入它，改由 frame-ancestors 'self' 限定），写 CSP 与 HTML。
func writeProbeFrame(c *gin.Context, portalOrigin string, domains []string) {
	filtered := make([]string, 0, len(domains))
	for _, d := range domains {
		if portalOrigin != "" && probeOriginRE.MatchString(d) {
			filtered = append(filtered, d)
		}
	}
	payload, err := json.Marshal(probeFrameData{
		Domains:      filtered,
		TimeoutMS:    probeFrameTimeoutMS,
		PortalOrigin: portalOrigin,
	})
	if err != nil {
		httpx.Fail(c, errs.Wrap(errs.Internal, err))
		return
	}
	body := "<!doctype html><html><head><meta charset=\"utf-8\"><meta name=\"referrer\" content=\"no-referrer\"><title>probe</title></head><body><script type=\"application/json\" id=\"probe-data\">" +
		string(payload) + "</script><script>" + probeFrameScript + "</script></body></html>"
	c.Writer.Header().Del("X-Frame-Options")
	c.Writer.Header().Set("Content-Security-Policy", probeFrameCSP(filtered))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(body))
}
