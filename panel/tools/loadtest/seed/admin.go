package seed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

type jsonObject = map[string]any

// apiError 是后台回了非期望状态码；Code 取自 httpx 错误信封 {"error":{"code":...}}。
type apiError struct {
	Method, Path string
	Status       int
	Code         string
	Body         string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d %s: %s", e.Method, e.Path, e.Status, e.Code, e.Body)
}

type adminClient struct {
	base     string
	email    string
	password string
	http     *http.Client
	interval time.Duration
	// ip / ipHeaders 非空时每个请求带上这个虚构来源地址（多会话并行造数时各会话各占一个 IP，
	// 后台每 IP 每分钟 240 次的限流各算各的）；为空则不带，来源就是连接本身
	ip        string
	ipHeaders []string

	mu       sync.Mutex
	token    string
	lastCall time.Time
	// Calls 计入每一次真正发出的请求（含重试），进阶段耗时的 count
	Calls int
}

func newAdminClient(base, email, password string, interval time.Duration) *adminClient {
	return &adminClient{
		base: base, email: email, password: password, interval: interval,
		http: &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *adminClient) login(ctx context.Context) error {
	out, err := c.send(ctx, http.MethodPost, "/v1/auth/login",
		jsonObject{"email": c.email, "password": c.password}, "", "")
	if err != nil {
		return fmt.Errorf("admin login: %w", err)
	}
	tok, err := str(out, "access_token")
	if err != nil {
		return err
	}
	c.token = tok
	return nil
}

// reauth 给同一会话重盖「刚确认过本人」的时间戳：签发接入令牌与套餐发布都要求 15 分钟内重认证过。
func (c *adminClient) reauth(ctx context.Context) error {
	out, err := c.send(ctx, http.MethodPost, "/v1/auth/reauth", jsonObject{"password": c.password}, c.token, "")
	if err != nil {
		return fmt.Errorf("admin reauth: %w", err)
	}
	tok, err := str(out, "access_token")
	if err != nil {
		return err
	}
	c.token = tok
	return nil
}

// call 发一个已登录的后台请求。写请求一律带新的幂等键；同一次 call 内的重试复用它，
// 后台据此把网络重试识别成同一次操作。expect 为空时只接受 200 / 201。
func (c *adminClient) call(ctx context.Context, method, path string, body any, expect ...int) (jsonObject, error) {
	idem := ""
	if method != http.MethodGet {
		idem = uuid.NewString()
	}
	reauthed := false
	for attempt := 0; ; attempt++ {
		out, err := c.send(ctx, method, path, body, c.token, idem, expect...)
		var ae *apiError
		if !errors.As(err, &ae) {
			return out, err
		}
		switch {
		case ae.Code == "reauth_required" && !reauthed:
			reauthed = true
			if err := c.reauth(ctx); err != nil {
				return nil, err
			}
		case ae.Status == http.StatusUnauthorized && attempt == 0:
			if err := c.login(ctx); err != nil {
				return nil, err
			}
		default:
			return nil, err
		}
	}
}

// send 是一次带节流与 429 退避的往返。
func (c *adminClient) send(ctx context.Context, method, path string, body any, token, idem string, expect ...int) (jsonObject, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	if len(expect) == 0 {
		expect = []int{http.StatusOK, http.StatusCreated}
	}
	for tries := 0; ; tries++ {
		if err := c.pace(ctx); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if idem != "" {
			req.Header.Set("Idempotency-Key", idem)
		}
		req.Header.Set("User-Agent", "pandora-loadtest-seed")
		if c.ip != "" {
			for _, h := range c.ipHeaders {
				req.Header.Set(h, c.ip)
			}
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", method, path, err)
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("%s %s: read body: %w", method, path, err)
		}
		c.mu.Lock()
		c.Calls++
		c.mu.Unlock()
		if resp.StatusCode == http.StatusTooManyRequests && tries < 20 {
			if err := sleepCtx(ctx, retryAfter(resp.Header.Get("Retry-After"))); err != nil {
				return nil, err
			}
			continue
		}
		for _, want := range expect {
			if resp.StatusCode == want {
				out := jsonObject{}
				if len(bytes.TrimSpace(raw)) > 0 {
					if err := json.Unmarshal(raw, &out); err != nil {
						return nil, fmt.Errorf("%s %s: decode: %w", method, path, err)
					}
				}
				return out, nil
			}
		}
		return nil, newAPIError(method, path, resp.StatusCode, raw)
	}
}

func newAPIError(method, path string, status int, raw []byte) *apiError {
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &env)
	snippet := string(raw)
	if len(snippet) > 400 {
		snippet = snippet[:400]
	}
	return &apiError{Method: method, Path: path, Status: status, Code: env.Error.Code, Body: snippet}
}

// pace 让相邻两次后台请求至少隔 interval。
func (c *adminClient) pace(ctx context.Context) error {
	c.mu.Lock()
	wait := time.Until(c.lastCall.Add(c.interval))
	if wait < 0 {
		wait = 0
	}
	c.lastCall = time.Now().Add(wait)
	c.mu.Unlock()
	return sleepCtx(ctx, wait)
}

func retryAfter(h string) time.Duration {
	if s, err := strconv.Atoi(h); err == nil && s > 0 && s <= 120 {
		return time.Duration(s) * time.Second
	}
	return 5 * time.Second
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ---------------------------------------------------------------------------
// 响应取值：路径用点分隔，取不到就报带响应片段的错，而不是悄悄给零值
// ---------------------------------------------------------------------------

func lookup(o jsonObject, path string) any {
	var v any = o
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v = m[path[start:i]]
			start = i + 1
		}
	}
	return v
}

func str(o jsonObject, path string) (string, error) {
	if s, ok := lookup(o, path).(string); ok && s != "" {
		return s, nil
	}
	return "", fmt.Errorf("response lacks string field %s: %.300s", path, mustJSON(o))
}

func num(o jsonObject, path string) (int64, error) {
	if f, ok := lookup(o, path).(float64); ok {
		return int64(f), nil
	}
	return 0, fmt.Errorf("response lacks numeric field %s: %.300s", path, mustJSON(o))
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
