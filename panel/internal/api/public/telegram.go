package public

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Telegram 绑定（对标 Xboard user/telegram/getBotInfo）。

func (h *handlers) telegramInfo(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Notify.BindingInfo(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) telegramBindCode(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	out, err := h.d.Notify.IssueBindCode(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}

func (h *handlers) telegramUnbind(w http.ResponseWriter, r *http.Request) {
	p := httpx.PrincipalFrom(r.Context())
	if err := h.d.Notify.Unbind(r.Context(),
		httpx.TenantIDFrom(r.Context()), p.UserID); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, map[string]any{"unbound": true})
}

// telegramUpdate 是 Bot 的 webhook 入口。
//
// 鉴权靠 URL 里的 secret 段：Telegram 只会把更新推给我们设置的那个地址，
// 所以地址本身就是凭证。这也是 Telegram 官方推荐的做法。
//
// 无论内部发生什么，一律回 200 —— Telegram 对非 2xx 会不断重投，
// 而一条它永远处理不了的消息（比如格式不对）重投多少次都一样，
// 只会把我们的日志刷满。
func (h *handlers) telegramUpdate(w http.ResponseWriter, r *http.Request) {
	defer func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}()

	// 对缺失、读取失败和不匹配统一回同一个空信息响应，既不泄露 secret
	// 是否存在，也不让比较耗时暴露可利用的前缀信息。
	cfg, err := notify.LoadTelegramConfig(r.Context(), h.d.Pool, h.d.Envelope,
		httpx.TenantIDFrom(r.Context()))
	provided := chi.URLParam(r, "secret")
	providedHash, expectedHash := sha256.Sum256([]byte(provided)), sha256.Sum256([]byte(cfg.WebhookSecret))
	if err != nil || cfg.WebhookSecret == "" ||
		subtle.ConstantTimeCompare(providedHash[:], expectedHash[:]) != 1 {
		return
	}

	// 限制读取长度：webhook 是公网可达的，不限就等于开了个内存放大器
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return
	}
	var upd struct {
		Message struct {
			Text string `json:"text"`
			Chat struct {
				ID       int64  `json:"id"`
				Username string `json:"username"`
			} `json:"chat"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &upd); err != nil {
		return
	}

	text := strings.TrimSpace(upd.Message.Text)
	if text == "" || upd.Message.Chat.ID == 0 {
		return
	}
	// 支持 "/start ABCD1234" 和直接发码两种形式：
	// 深链点进来是前者，手工粘贴是后者。
	text = strings.TrimPrefix(text, "/start")
	code := strings.ToUpper(strings.TrimSpace(text))

	tenantID := httpx.TenantIDFrom(r.Context())
	userID, bindErr := h.d.Notify.ConfirmBind(r.Context(), tenantID, code,
		upd.Message.Chat.ID, upd.Message.Chat.Username)

	// 无论成败都回一条消息，否则用户对着 Telegram 干等
	reply := "绑定成功，之后的到期提醒和流量预警会发到这里。"
	if bindErr != nil {
		reply = "绑定失败：" + bindErr.Error()
		h.d.Log.Info("telegram 绑定失败", "chat_id", upd.Message.Chat.ID,
			"err", bindErr.Error())
	} else {
		h.d.Log.Info("telegram 绑定成功", "user_id", userID)
	}
	if sender := h.d.TelegramSender; sender != nil {
		_ = sender.Send(r.Context(), itoa64(upd.Message.Chat.ID), "", reply)
	}
}

func itoa64(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [24]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// appearance 返回主题与插槽，供用户端渲染。
//
// 免鉴权：登录页也要按站点主题显示，而这里不含任何用户数据。
func (h *handlers) appearance(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Appearance.Public(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, out)
}
