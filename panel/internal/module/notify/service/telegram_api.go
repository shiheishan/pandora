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
	// SendMessage 给 chatID 发一条纯文本消息（不用 parse_mode，关掉链接预览），返回 Telegram 给的 message_id。
	SendMessage(ctx context.Context, token string, chatID int64, text string) (int64, error)
	// SetWebhook 把 webhook 设成 url，回调头里带 secret；只收 allowedUpdates 列出的更新类型。
	// dropPending 为 true 时丢弃 Telegram 那边排队未推的更新（换 token、换 secret 时用）；只改 allowedUpdates 时传 false。
	SetWebhook(ctx context.Context, token, url, secret string, allowedUpdates []string, dropPending bool) error
	// SetMyCommands 设置 bot 在私聊里的命令菜单（scope all_private_chats）。
	SetMyCommands(ctx context.Context, token string, cmds []botCommand) error
	// ApproveChatJoinRequest / DeclineChatJoinRequest 批准 / 拒绝 userID 加入 chatID 的申请。
	ApproveChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error
	DeclineChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error
}

// botCommand 是命令菜单的一项。
type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
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
	OK          bool            `json:"ok"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Result      json.RawMessage `json:"result"`
}

func (c *telegramClient) SendMessage(ctx context.Context, token string, chatID int64, text string) (int64, error) {
	text = truncateRunes(text, 4096)
	body, err := json.Marshal(map[string]any{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	})
	if err != nil {
		return 0, err
	}
	raw, err := c.post(ctx, token, "sendMessage", body)
	if err != nil {
		return 0, err
	}
	var msg struct {
		MessageID int64 `json:"message_id"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &msg) != nil || msg.MessageID <= 0 {
		// 2xx 但 result 读不出 message_id：消息多半已经发出，记永久失败、不重发，免得群里收到重复消息
		return 0, &TelegramError{Status: http.StatusOK, Detail: "回包缺少 message_id", Permanent: true}
	}
	return msg.MessageID, nil
}

func (c *telegramClient) SetWebhook(ctx context.Context, token, hookURL, secret string, allowedUpdates []string, dropPending bool) error {
	if allowedUpdates == nil {
		allowedUpdates = []string{"message"}
	}
	body, err := json.Marshal(map[string]any{
		"url":                  hookURL,
		"secret_token":         secret,
		"allowed_updates":      allowedUpdates,
		"drop_pending_updates": dropPending,
	})
	if err != nil {
		return err
	}
	_, err = c.post(ctx, token, "setWebhook", body)
	return err
}

func (c *telegramClient) SetMyCommands(ctx context.Context, token string, cmds []botCommand) error {
	body, err := json.Marshal(map[string]any{
		"commands": cmds,
		"scope":    map[string]string{"type": "all_private_chats"},
	})
	if err != nil {
		return err
	}
	_, err = c.post(ctx, token, "setMyCommands", body)
	return err
}

func (c *telegramClient) ApproveChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	})
	if err != nil {
		return err
	}
	_, err = c.post(ctx, token, "approveChatJoinRequest", body)
	return err
}

func (c *telegramClient) DeclineChatJoinRequest(ctx context.Context, token string, chatID, userID int64) error {
	body, err := json.Marshal(map[string]any{
		"chat_id": chatID,
		"user_id": userID,
	})
	if err != nil {
		return err
	}
	_, err = c.post(ctx, token, "declineChatJoinRequest", body)
	return err
}

func (c *telegramClient) post(ctx context.Context, token, method string, body []byte) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	urlStr := c.base + "/bot" + token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		// 错误文本带完整 URL，也就带着 token
		return nil, c.wrapConnErr(err, token)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, c.wrapConnErr(err, token)
	}
	defer resp.Body.Close()

	return c.readResponse(resp, token)
}

// tgResponseMax 是回包读取上限。sendMessage 成功时 result 带回整条消息，4096 字的中文按 \uXXXX 转义约 24 KB，
// 加上 entities、chat、from 也远小于 64 KiB。
const tgResponseMax = 64 << 10

func (c *telegramClient) readResponse(resp *http.Response, token string) (json.RawMessage, error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, tgResponseMax))
	if err != nil {
		return nil, err
	}
	status := resp.StatusCode
	ok2xx := status >= 200 && status < 300

	var api tgAPIResponse
	var parseErr error
	if len(raw) > 0 {
		parseErr = json.Unmarshal(raw, &api)
	}
	if ok2xx && (len(raw) == 0 || parseErr != nil) {
		// 2xx 却解析不了：请求已被受理，重发可能造成重复消息，按永久失败处理
		return nil, &TelegramError{Status: status, Detail: fmt.Sprintf("HTTP %d 回包无法解析", status), Permanent: true}
	}
	if ok2xx && api.OK {
		return api.Result, nil
	}

	desc := api.Description
	if desc == "" {
		desc = http.StatusText(status)
	}
	desc = truncateRunes(desc, 200)
	detail := fmt.Sprintf("HTTP %d %s", status, desc)
	perm := status == 400 || status == 401 || status == 403 || status == 404
	return nil, &TelegramError{Status: status, Detail: detail, Permanent: perm}
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
