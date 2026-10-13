package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// telegramAPIBase 是 Telegram Bot API 的地址；测试换成 httptest 服务器。
const telegramAPIBase = "https://api.telegram.org"

// telegramAPI 是用到的 Bot API 方法；生产用 telegramClient，测试用假实现。
type telegramAPI interface {
	// SendMessage 给 chatID 发一条纯文本消息（不用 parse_mode，关掉链接预览）。
	SendMessage(ctx context.Context, token string, chatID int64, text string) error
	// SetWebhook 把 bot 的 webhook 设成 url，Telegram 回调时在 X-Telegram-Bot-Api-Secret-Token 头里带 secret；只收 message 更新。
	SetWebhook(ctx context.Context, token, url, secret string) error
}

// TelegramError 是 Bot API 调用失败。Detail 可以写日志、写库、给管理员看（不含 Token 与请求地址）；
// Permanent 为 true 表示重试也没用（Token 错、chat 不存在、被用户拉黑）。
type TelegramError struct {
	Status    int // HTTP 状态码；连不上时为 0
	Detail    string
	Permanent bool
}

func (e *TelegramError) Error() string { return "Telegram：" + e.Detail }

type telegramClient struct {
	base string
	hc   *http.Client
}

func newTelegramClient(base string) *telegramClient {
	return &telegramClient{
		base: base,
		hc:   &http.Client{Timeout: 15 * time.Second},
	}
}

type tgAPIResponse struct {
	OK          bool   `json:"ok"`
	ErrorCode   int    `json:"error_code"`
	Description string `json:"description"`
}

func (c *telegramClient) SendMessage(ctx context.Context, token string, chatID int64, text string) error {
	text = truncateRunes(text, 4096)
	body, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}
	return c.post(ctx, token, "sendMessage", body)
}

func (c *telegramClient) SetWebhook(ctx context.Context, token, hookURL, secret string) error {
	body, err := json.Marshal(map[string]any{
		"url":                  hookURL,
		"secret_token":         secret,
		"allowed_updates":      []string{"message"},
		"drop_pending_updates": true,
	})
	if err != nil {
		return err
	}
	return c.post(ctx, token, "setWebhook", body)
}

func (c *telegramClient) post(ctx context.Context, token, method string, body []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	urlStr := c.base + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		// 错误文本带完整 URL，也就带着 token
		return c.wrapConnErr(err, token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return c.wrapConnErr(err, token)
	}
	defer resp.Body.Close()

	return c.readResponse(resp, token)
}

func (c *telegramClient) readResponse(resp *http.Response, token string) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if err != nil {
		return err
	}
	var api tgAPIResponse
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &api)
	}

	status := resp.StatusCode
	if status >= 200 && status < 300 && api.OK {
		return nil
	}

	desc := api.Description
	if desc == "" {
		desc = http.StatusText(status)
	}
	desc = truncateRunes(desc, 200)
	detail := fmt.Sprintf("HTTP %d %s", status, desc)
	perm := status == 400 || status == 401 || status == 403 || status == 404
	return &TelegramError{Status: status, Detail: detail, Permanent: perm}
}

func (c *telegramClient) wrapConnErr(err error, token string) *TelegramError {
	msg := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		msg = urlErr.Err.Error()
	}
	msg = strings.ReplaceAll(msg, token, "***")
	return &TelegramError{Status: 0, Detail: "连接失败：" + msg, Permanent: false}
}
