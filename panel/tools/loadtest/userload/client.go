package userload

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// ---------------------------------------------------------------------------
// 请求与响应
// ---------------------------------------------------------------------------

// gateway 是一个网关的基址。name 进端点名（public / admin），base 不带结尾斜杠；
// 后台基址里的秘密前缀只活在 base 里，端点名用的是路径模板，前缀不会进报告。
type gateway struct {
	name string
	base string
}

func newGateway(name, base string) *gateway {
	return &gateway{name: name, base: strings.TrimRight(base, "/")}
}

// request 是一次要发出去的请求。path 是实际路径（含查询串），tmpl 是进报告的路径模板：
// id 换成 {id}、订阅令牌换成 {token}、邮箱换成 {q}，报告里不出现任何一个真实标识。
type request struct {
	gw      *gateway
	method  string
	path    string
	tmpl    string
	ip      string
	ua      string
	token   string
	body    any
	idemKey string
	// flag 给这一类请求补自定义标签（订阅的格式核对、诱饵 404）；返回空串时用通用标签。
	flag func(response) string
}

type response struct {
	status  int
	header  http.Header
	body    []byte
	code    string // 错误体 {"error":{"code":...}} 里的码；非 JSON 错误为空
	err     error
	latency time.Duration
}

// maxBody 是读响应体的上限：200 个节点的订阅正文在百 KB 量级，8 MiB 足够且防失控。
const maxBody = 8 << 20

// browserUA 是门户与后台请求的 UA：用户在浏览器里打开页面，会话表里记的就是它。
const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"

type client struct {
	hc      *http.Client
	timeout time.Duration
	// ipHeaders 是承载模拟来源 IP 的请求头，缺省只有 X-Real-IP（面板 httpx.ClientIP 只认它）。
	// 经 nginx 打面板时，nginx 的 realip 只认一个头（生产是 CF-Connecting-IP）：测试机上把压测机
	// 加进 set_real_ip_from 并同时带上 CF-Connecting-IP，nginx 就会把它还原成 $remote_addr，
	// 再经 proxy_set_header X-Real-IP $remote_addr 交给面板，nginx 自己的 limit_req 也按模拟 IP 计。
	ipHeaders []string
}

// newClient 的连接池按并发上限开：开环调度下在途请求最多 maxInflight 个，
// 空闲连接数跟它对齐，才不会每个请求都重新握手（那测的就是压测机的 TLS 了）。
func newClient(maxInflight int, timeout time.Duration, ipHeaders []string) *client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = maxInflight
	tr.MaxIdleConnsPerHost = maxInflight
	tr.IdleConnTimeout = 90 * time.Second
	return &client{
		hc: &http.Client{
			Transport: tr,
			// 不跟随跳转：面板 API 不该跳转，跳了就是配置错（例如漏了后台前缀），要原样看到
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		timeout:   timeout,
		ipHeaders: ipHeaders,
	}
}

func endpointName(gw, method, tmpl string) string { return gw + ":" + method + " " + tmpl }

// do 发出请求并记一条观测。耗时含读完响应体：用户感受到的是整份响应到手，
// 订阅正文的渲染与传输都算在里面。
func (c *client) do(ctx context.Context, rec *ltkit.Recorder, rq request) response {
	resp := c.send(ctx, rq)
	if rec != nil {
		rec.Observe(ltkit.Observation{
			Endpoint: endpointName(rq.gw.name, rq.method, rq.tmpl),
			Status:   resp.status,
			Latency:  resp.latency,
			Err:      resp.err,
			Flag:     flagOf(rq, resp),
		})
	}
	return resp
}

func (c *client) send(ctx context.Context, rq request) response {
	var rdr io.Reader
	if rq.body != nil {
		b, err := json.Marshal(rq.body)
		if err != nil {
			return response{err: err}
		}
		rdr = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, rq.method, rq.gw.base+rq.path, rdr)
	if err != nil {
		return response{err: err}
	}
	if rq.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.HasPrefix(rq.path, "/v1/") {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", rq.ua)
	if rq.ip != "" {
		for _, h := range c.ipHeaders {
			req.Header.Set(h, rq.ip)
		}
	}
	if rq.token != "" {
		req.Header.Set("Authorization", "Bearer "+rq.token)
	}
	if rq.idemKey != "" {
		req.Header.Set("Idempotency-Key", rq.idemKey)
	}

	start := time.Now()
	hr, err := c.hc.Do(req)
	if err != nil {
		return response{err: err, latency: time.Since(start)}
	}
	body, rerr := io.ReadAll(io.LimitReader(hr.Body, maxBody))
	_ = hr.Body.Close()
	out := response{status: hr.StatusCode, header: hr.Header, body: body, latency: time.Since(start)}
	if rerr != nil {
		// 头到了、体没读完：按无响应记，否则一个被截断的 200 会混进成功里
		return response{err: rerr, latency: out.latency}
	}
	if hr.StatusCode >= 400 {
		out.code = errorCode(body)
	}
	return out
}

// errorCode 取 platform/httpx 错误体里的 error.code。
func errorCode(body []byte) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	return e.Error.Code
}

// flagOf 给观测打标签。429 按来源分开：中间件限流回 JSON rate_limited（rl_mw），
// 订阅分发的每凭据每小时上限回纯文本（rl_sub_hourly）——两者的含义与对策完全不同。
func flagOf(rq request, resp response) string {
	if rq.flag != nil {
		if f := rq.flag(resp); f != "" {
			return f
		}
	}
	switch {
	case resp.status == http.StatusTooManyRequests && resp.code == "rate_limited":
		return "rl_mw"
	case resp.status == http.StatusTooManyRequests:
		return "rl_sub_hourly"
	case resp.status == http.StatusUnauthorized:
		return "unauthorized"
	}
	return ""
}

// ---------------------------------------------------------------------------
// 登录与重认证
// ---------------------------------------------------------------------------

type credentials struct {
	email    string
	password string
}

// login 走 POST /v1/auth/login（门户与后台同一路径、同一请求体），返回访问令牌。
// 两边都没有验证码与二次验证；令牌默认 30 天有效（AEGIS_ACCESS_TOKEN_TTL），一次压测内不用刷新。
func (c *client) login(ctx context.Context, rec *ltkit.Recorder, gw *gateway, cr credentials, ip string) (string, response) {
	resp := c.do(ctx, rec, request{
		gw: gw, method: http.MethodPost, path: "/v1/auth/login", tmpl: "/v1/auth/login",
		ip: ip, ua: browserUA,
		body: map[string]string{"email": cr.email, "password": cr.password},
	})
	return accessToken(resp), resp
}

// reauth 走后台 POST /v1/auth/reauth：拿口令换一枚带新重认证时间的令牌（窗口 15 分钟）。
func (c *client) reauth(ctx context.Context, rec *ltkit.Recorder, gw *gateway, token, password, ip string) (string, response) {
	resp := c.do(ctx, rec, request{
		gw: gw, method: http.MethodPost, path: "/v1/auth/reauth", tmpl: "/v1/auth/reauth",
		ip: ip, ua: browserUA, token: token,
		body: map[string]string{"password": password},
	})
	return accessToken(resp), resp
}

func accessToken(resp response) string {
	if resp.status != http.StatusOK {
		return ""
	}
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(resp.body, &out) != nil {
		return ""
	}
	return out.AccessToken
}

// describe 把一次失败的响应说成一句话（不带响应体原文：里面可能有邮箱之类的回显）。
func describe(resp response) string {
	if resp.err != nil {
		return "transport: " + resp.err.Error()
	}
	if resp.code != "" {
		return fmt.Sprintf("HTTP %d %s", resp.status, resp.code)
	}
	return fmt.Sprintf("HTTP %d", resp.status)
}
