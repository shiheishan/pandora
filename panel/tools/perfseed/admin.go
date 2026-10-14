package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const adminHTTPTimeout = 30 * time.Second
const adminReauthInterval = 10 * time.Minute

// adminClient 调用后台 HTTP API。
type adminClient struct {
	base     string
	email    string
	password string
	http     *http.Client
	token    string
	authedAt time.Time
}

func newAdminClient(base, email, password string) *adminClient {
	return &adminClient{
		base:     strings.TrimSuffix(base, "/"),
		email:    email,
		password: password,
		http: &http.Client{
			Timeout: adminHTTPTimeout,
		},
	}
}

// auth 先 login 再 reauth，后续请求用 reauth 返回的令牌。
func (c *adminClient) auth(ctx context.Context) error {
	var loginOut struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.doOnce(ctx, http.MethodPost, "/v1/auth/login",
		map[string]string{"email": c.email, "password": c.password}, "", http.StatusOK, &loginOut); err != nil {
		return err
	}
	if loginOut.AccessToken == "" {
		return &apiError{
			Method:  http.MethodPost,
			Path:    "/v1/auth/login",
			Status:  http.StatusOK,
			Message: "响应缺少 access_token",
		}
	}
	var reauthOut struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.doOnceWithToken(ctx, http.MethodPost, "/v1/auth/reauth",
		map[string]string{"password": c.password}, "", loginOut.AccessToken, http.StatusOK, &reauthOut); err != nil {
		return err
	}
	if reauthOut.AccessToken == "" {
		return &apiError{
			Method:  http.MethodPost,
			Path:    "/v1/auth/reauth",
			Status:  http.StatusOK,
			Message: "响应缺少 access_token",
		}
	}
	c.token = reauthOut.AccessToken
	c.authedAt = time.Now()
	return nil
}

func (c *adminClient) ensureFreshAuth(ctx context.Context) error {
	if c.token == "" || time.Since(c.authedAt) > adminReauthInterval {
		return c.auth(ctx)
	}
	return nil
}

func (c *adminClient) do(ctx context.Context, method, path string, body any, idemKey string, wantStatus int, out any) error {
	if err := c.ensureFreshAuth(ctx); err != nil {
		return err
	}
	err := c.doOnce(ctx, method, path, body, idemKey, wantStatus, out)
	if err == nil {
		return nil
	}
	var ae *apiError
	if errorsAsAPI(err, &ae) && ae.Status == http.StatusUnauthorized {
		if authErr := c.auth(ctx); authErr != nil {
			return authErr
		}
		return c.doOnce(ctx, method, path, body, idemKey, wantStatus, out)
	}
	return err
}

func (c *adminClient) doOnce(ctx context.Context, method, path string, body any, idemKey string, wantStatus int, out any) error {
	return c.doOnceWithToken(ctx, method, path, body, idemKey, c.token, wantStatus, out)
}

func (c *adminClient) doOnceWithToken(ctx context.Context, method, path string, body any, idemKey, token string, wantStatus int, out any) error {
	const max429Retries = 5
	for attempt := 0; attempt <= max429Retries; attempt++ {
		err := c.roundTrip(ctx, method, path, body, idemKey, token, wantStatus, out)
		if err == nil {
			return nil
		}
		var ae *apiError
		if !errorsAsAPI(err, &ae) || ae.Status != http.StatusTooManyRequests {
			return err
		}
		if attempt == max429Retries {
			return err
		}
		// RetryAfter 已由 parseRetryAfter 规整：缺省 2 秒，封顶 30 秒；0 表示立刻重发
		wait := ae.RetryAfter
		if wait < 0 {
			wait = 2
		}
		if wait > 30 {
			wait = 30
		}
		timer := time.NewTimer(time.Duration(wait) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (c *adminClient) roundTrip(ctx context.Context, method, path string, body any, idemKey, token string, wantStatus int, out any) error {
	reqID := "perfseed-" + uuid.NewString()
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("编码请求体：%w", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bodyReader)
	if err != nil {
		return err
	}
	req.Header.Set("X-Request-ID", reqID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode != wantStatus {
		return newAPIErrorFromResponse(method, path, resp.StatusCode, resp.Header, reqID, respBody)
	}
	if out != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("解码响应：%w", err)
		}
	}
	return nil
}

// apiError 表示后台返回了非期望状态码。
type apiError struct {
	Method     string
	Path       string
	Status     int
	ReqID      string
	Code       string
	Message    string
	RetryAfter int // 仅 429 时由解析 Retry-After 填入
}

func (e *apiError) Error() string {
	parts := []string{
		fmt.Sprintf("%s %s：HTTP %d", e.Method, e.Path, e.Status),
	}
	if e.ReqID != "" {
		parts = append(parts, fmt.Sprintf("req_id=%s", e.ReqID))
	}
	if e.Code != "" {
		parts = append(parts, fmt.Sprintf("code=%s", e.Code))
	}
	if e.Message != "" {
		parts = append(parts, fmt.Sprintf("message=%s", e.Message))
	}
	return strings.Join(parts, "，")
}

type apiErrEnvelope struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		RequestID string `json:"request_id"`
	} `json:"error"`
}

func newAPIErrorFromResponse(method, path string, status int, hdr http.Header, sentReqID string, body []byte) error {
	e := &apiError{
		Method: method,
		Path:   path,
		Status: status,
	}
	if status == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(hdr.Get("Retry-After"))
	}
	var env apiErrEnvelope
	if len(body) > 0 {
		_ = json.Unmarshal(body, &env)
		e.Code = env.Error.Code
		if env.Error.Message != "" {
			e.Message = env.Error.Message
		}
	}
	e.ReqID = hdr.Get("X-Request-ID")
	if e.ReqID == "" {
		e.ReqID = env.Error.RequestID
	}
	if e.ReqID == "" {
		e.ReqID = sentReqID
	}
	if e.Message == "" {
		e.Message = http.StatusText(status)
	}
	return e
}

func errorsAsAPI(err error, out **apiError) bool {
	if err == nil {
		return false
	}
	if ae, ok := err.(*apiError); ok {
		*out = ae
		return true
	}
	return false
}

func parseRetryAfter(v string) int {
	if v == "" {
		return 2
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < 0 {
		return 2
	}
	if n > 30 {
		return 30
	}
	return n
}
