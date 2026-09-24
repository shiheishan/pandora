// [INPUT]: 依赖 plugin_hooks / plugin_hook_deliveries（00051，00081 加 last_duration_ms），依赖 platform 的 audit/crypto/db/httpx
// [OUTPUT]: 对外提供 EventInfo 与 Events 事件目录（小写 name / desc）、KnownEvent、Service、New，钩子增删查、Emit 入队、Dispatch / StartScanner 投递、Deliveries 投递记录、TestHook 同步测试
// [POS]: domain/plugin 的主体：出站 webhook 的配置、签名投递与重试；每次尝试记往返耗时（timedPost），emit.go 为各业务事件的薄封装
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 插件钩子：面板把事件以签名 HTTP 请求推给插件自己的服务。
//
// 为什么不做「上传插件包然后加载执行」：那种设计里插件跑在面板进程里，
// 一个写得差的插件能拖垮整个面板，一个恶意插件直接拿到数据库连接和主密钥，
// 而后台上传入口就变成了任意代码执行入口。出站 webhook 把插件推到进程外，
// 代价是插件要自己起个服务，换来的是插件崩了面板不崩、插件被攻破了
// 数据库还在。

//------------------------------------------------------------------------------
// 事件目录
//------------------------------------------------------------------------------

// Events 是可订阅的事件。
//
// 白名单放在代码里：允许订阅任意字符串的话，管理员打错一个字母
// 就是一个永远不触发的钩子，而且没有任何地方会提示他。
// EventInfo 的 JSON 键是小写 name / desc（契约后台-09；此前匿名结构体输出大写键）。
type EventInfo struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

var Events = []EventInfo{
	{"user.registered", "用户完成注册"},
	{"order.created", "订单创建"},
	{"order.paid", "订单支付成功"},
	{"order.cancelled", "订单取消"},
	{"subscription.provisioned", "订阅开通或续期"},
	{"subscription.expiring", "订阅即将到期"},
	{"subscription.expired", "订阅已过期"},
	{"traffic.exhausted", "流量用尽"},
	{"ticket.created", "用户提交工单"},
	{"giftcard.redeemed", "礼品卡兑换"},
}

func KnownEvent(name string) bool {
	for _, e := range Events {
		if e.Name == name {
			return true
		}
	}
	return false
}

//------------------------------------------------------------------------------
// 服务
//------------------------------------------------------------------------------

type Service struct {
	pool     *db.Pool
	envelope *crypto.Envelope
	client   *http.Client
	// devMode 时才允许把钩子指向内网地址。生产必须为 false，
	// 否则一个能改钩子的账号就能把面板当成内网扫描器（SSRF）。
	devMode bool
}

func New(pool *db.Pool, envelope *crypto.Envelope, devMode bool) *Service {
	return &Service{
		pool:     pool,
		envelope: envelope,
		devMode:  devMode,
		client: &http.Client{
			Timeout: 30 * time.Second,
			// 不跟随跳转：一个允许的外网地址可以 302 到 169.254.169.254
			// 这类云元数据端点，那样前面的地址校验就白做了。
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

type Hook struct {
	ID          string   `json:"id"`
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Enabled     bool     `json:"enabled"`
	Events      []string `json:"events"`
	EndpointURL string   `json:"endpoint_url"`
	HasSecret   bool     `json:"has_secret"`
	TimeoutMS   int      `json:"timeout_ms"`
	MaxAttempts int      `json:"max_attempts"`
	QueuedCount int      `json:"queued_count"`
	FailedCount int      `json:"failed_count"`
	// SentCount7d 与 FailedCount 同一个 7 天窗口，前端算成功率 sent / (sent + failed)
	SentCount7d int     `json:"sent_count_7d"`
	LastSentAt  *string `json:"last_sent_at,omitempty"`
}

func (s *Service) List(ctx context.Context, tenantID string) ([]Hook, error) {
	out := []Hook{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT h.id, h.code, h.name, h.description, h.enabled, h.events,
			       h.endpoint_url,
			       (h.secret_encrypted IS NOT NULL AND length(h.secret_encrypted) > 0),
			       h.timeout_ms, h.max_attempts,
			       (SELECT count(*) FROM plugin_hook_deliveries d
			         WHERE d.tenant_id=h.tenant_id AND d.hook_id=h.id AND d.status='queued'),
			       (SELECT count(*) FROM plugin_hook_deliveries d
			         WHERE d.tenant_id=h.tenant_id AND d.hook_id=h.id AND d.status='failed'
			           AND d.created_at > now() - interval '7 days'),
			       (SELECT to_char(max(d.sent_at),'YYYY-MM-DD HH24:MI')
			          FROM plugin_hook_deliveries d
			         WHERE d.tenant_id=h.tenant_id AND d.hook_id=h.id AND d.status='sent'),
			       (SELECT count(*) FROM plugin_hook_deliveries d
			         WHERE d.tenant_id=h.tenant_id AND d.hook_id=h.id AND d.status='sent'
			           AND d.created_at > now() - interval '7 days')
			  FROM plugin_hooks h WHERE h.tenant_id = $1 ORDER BY h.code`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hook
			if err := rows.Scan(&h.ID, &h.Code, &h.Name, &h.Description, &h.Enabled,
				&h.Events, &h.EndpointURL, &h.HasSecret, &h.TimeoutMS, &h.MaxAttempts,
				&h.QueuedCount, &h.FailedCount, &h.LastSentAt, &h.SentCount7d); err != nil {
				return err
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

type SaveHookInput struct {
	Code        string
	Name        string
	Description string
	Enabled     bool
	Events      []string
	EndpointURL string
	Secret      string // 空表示不修改
	TimeoutMS   int
	MaxAttempts int
	ActorID     string
}

// SaveHook 新建或修改一个钩子，返回新生成的密钥（仅新建且未指定时）。
func (s *Service) SaveHook(ctx context.Context, tenantID string, in SaveHookInput) (string, error) {
	in.Code = strings.ToLower(strings.TrimSpace(in.Code))
	in.Name = strings.TrimSpace(in.Name)
	in.EndpointURL = strings.TrimSpace(in.EndpointURL)

	fields := map[string]string{}
	if in.Code == "" {
		fields["code"] = "插件标识必填，小写字母开头"
	}
	if in.Name == "" {
		fields["name"] = "插件名称必填"
	}
	if err := s.validateEndpoint(in.EndpointURL); err != nil {
		fields["endpoint_url"] = err.Error()
	}
	for _, e := range in.Events {
		if !KnownEvent(e) {
			fields["events"] = "未知事件：" + e
		}
	}
	if in.Enabled && len(in.Events) == 0 {
		fields["events"] = "启用前至少要订阅一个事件，否则这个钩子永远不会触发"
	}
	if len(fields) > 0 {
		return "", httpx.Invalid(fields)
	}

	if in.TimeoutMS == 0 {
		in.TimeoutMS = 5000
	}
	if in.MaxAttempts == 0 {
		in.MaxAttempts = 5
	}

	// 没给密钥就生成一个：让插件在「不设签名」的状态下跑起来太容易了，
	// 而那意味着任何知道地址的人都能伪造事件。默认给上，逼着对方验签。
	generated := ""
	secret := in.Secret
	if secret == "" {
		var exists bool
		_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx,
				`SELECT count(*) > 0 FROM plugin_hooks WHERE tenant_id=$1 AND code=$2`,
				tenantID, in.Code).Scan(&exists)
		})
		if !exists {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return "", err
			}
			secret = "whsec_" + base64.RawURLEncoding.EncodeToString(b)
			generated = secret
		}
	}

	var sealed []byte
	if secret != "" {
		var err error
		sealed, err = s.envelope.Seal([]byte(secret), []byte("plugin_hook:"+in.Code))
		if err != nil {
			return "", err
		}
	}

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID},
		func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				INSERT INTO plugin_hooks
					(tenant_id, code, name, description, enabled, events,
					 endpoint_url, secret_encrypted, timeout_ms, max_attempts, created_by)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,nullif($11,'')::uuid)
				ON CONFLICT (tenant_id, code) DO UPDATE
				   SET name=EXCLUDED.name, description=EXCLUDED.description,
				       enabled=EXCLUDED.enabled, events=EXCLUDED.events,
				       endpoint_url=EXCLUDED.endpoint_url,
				       -- 空密钥表示不修改：界面上不回显已有密钥，
				       -- 提交时留空应当保留原值而不是把它清掉。
				       secret_encrypted=COALESCE(EXCLUDED.secret_encrypted,
				                                 plugin_hooks.secret_encrypted),
				       timeout_ms=EXCLUDED.timeout_ms,
				       max_attempts=EXCLUDED.max_attempts, updated_at=now()`,
				tenantID, in.Code, in.Name, in.Description, in.Enabled, in.Events,
				in.EndpointURL, sealed, in.TimeoutMS, in.MaxAttempts, in.ActorID)
			if db.IsUniqueViolation(err) {
				return httpx.New(httpx.CodeConflict, "该插件标识已存在")
			}
			if err != nil {
				return err
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &in.ActorID, Action: "plugin.hook.save",
				ResourceType: "plugin_hook", APIDomain: "admin",
				RequestID: httpx.RequestIDFrom(ctx),
				AfterDigest: map[string]any{
					"code": in.Code, "enabled": in.Enabled,
					"events": in.Events, "endpoint": in.EndpointURL},
			})
		})
	return generated, err
}

func (s *Service) DeleteHook(ctx context.Context, tenantID, code, actorID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID},
		func(tx pgx.Tx) error {
			ct, err := tx.Exec(ctx,
				`DELETE FROM plugin_hooks WHERE tenant_id=$1 AND code=$2`, tenantID, code)
			if err != nil {
				return err
			}
			if ct.RowsAffected() == 0 {
				return httpx.New(httpx.CodeNotFound, "插件不存在")
			}
			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actorID, Action: "plugin.hook.delete",
				ResourceType: "plugin_hook", APIDomain: "admin",
				RequestID:    httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{"code": code},
			})
		})
}

//------------------------------------------------------------------------------
// 地址校验（SSRF）
//------------------------------------------------------------------------------

// validateEndpoint 拦住指向内网的钩子地址。
//
// 面板能连到数据库、Valkey、云元数据服务和同机房的一切。一个能配钩子的
// 后台账号如果可以把地址填成 http://127.0.0.1:5433 或
// http://169.254.169.254/latest/meta-data/，那它拿到的就不只是面板权限了。
//
// 这里只做静态校验。DNS 重绑定（域名解析先返回公网再返回内网）挡不住 ——
// 要挡得在实际拨号时校验 IP。发送时用的 dialer 会再查一次，见 dialGuard。
func (s *Service) validateEndpoint(raw string) error {
	if raw == "" {
		return errors.New("回调地址必填")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("不是合法的地址")
	}
	if u.Scheme != "https" && !s.devMode {
		// 事件里会带用户邮箱、订单号这类东西，明文发出去等于沿途公开
		return errors.New("必须使用 https")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("只支持 http/https")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("地址里没有主机名")
	}
	if s.devMode {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return errors.New("不允许指向内网或本机地址")
		}
		return nil
	}
	// 域名：解析一次，任一结果落在内网就拒绝
	ips, err := net.LookupIP(host)
	if err != nil {
		return errors.New("域名解析不了，请检查地址")
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return errors.New("该域名解析到内网地址，不允许")
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() {
		return true
	}
	// 100.64.0.0/10：运营商级 NAT，云上常用作内部网段，
	// Go 的 IsPrivate 不覆盖它。
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1]&0xc0 == 64 {
			return true
		}
		// 169.254.169.254 已被 IsLinkLocalUnicast 覆盖，这里不再重复
	}
	return false
}

//------------------------------------------------------------------------------
// 事件投递
//------------------------------------------------------------------------------

// Emit 把一个事件排进所有订阅了它的钩子的队列。
//
// 只写队列不发送：业务事务里做网络请求，意味着对方慢一秒我们的
// 数据库事务就多持锁一秒。真正的发送交给扫描器。
//
// tx 是调用方的事务 —— 事件入队要和业务变更同生共死：订单回滚了却
// 已经通知了插件，插件那边就会有一笔不存在的订单。
func Emit(ctx context.Context, tx pgx.Tx, tenantID, event, dedupeKey string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO plugin_hook_deliveries
			(tenant_id, hook_id, event, dedupe_key, payload, max_attempts, next_retry_at)
		SELECT $1, h.id, $2, $3, $4::jsonb, h.max_attempts, now()
		  FROM plugin_hooks h
		 WHERE h.tenant_id = $1 AND h.enabled AND $2 = ANY(h.events)
		ON CONFLICT (tenant_id, hook_id, dedupe_key) DO NOTHING`,
		tenantID, event, dedupeKey, string(raw))
	return err
}

type dueDelivery struct {
	id        string
	hookID    string
	event     string
	payload   []byte
	attempts  int
	maxTries  int
	endpoint  string
	timeoutMS int
	code      string
	sealed    []byte
}

// Dispatch 发一批到期的投递，返回处理条数。
func (s *Service) Dispatch(ctx context.Context, tenantID string, limit int) (int, error) {
	if limit <= 0 {
		limit = 50
	}
	var batch []dueDelivery
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.id, d.hook_id, d.event, d.payload, d.attempts, d.max_attempts,
			       h.endpoint_url, h.timeout_ms, h.code, h.secret_encrypted
			  FROM plugin_hook_deliveries d
			  JOIN plugin_hooks h ON h.tenant_id=d.tenant_id AND h.id=d.hook_id
			 WHERE d.tenant_id=$1 AND d.status='queued'
			   AND d.next_retry_at <= now() AND h.enabled
			 ORDER BY d.next_retry_at
			 LIMIT $2
			 FOR UPDATE OF d SKIP LOCKED`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d dueDelivery
			if err := rows.Scan(&d.id, &d.hookID, &d.event, &d.payload, &d.attempts,
				&d.maxTries, &d.endpoint, &d.timeoutMS, &d.code, &d.sealed); err != nil {
				return err
			}
			batch = append(batch, d)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, err
	}

	for _, d := range batch {
		s.deliverOne(ctx, tenantID, d)
	}
	return len(batch), nil
}

func (s *Service) deliverOne(ctx context.Context, tenantID string, d dueDelivery) {
	secret := ""
	if len(d.sealed) > 0 {
		if plain, err := s.envelope.Open(d.sealed, []byte("plugin_hook:"+d.code)); err == nil {
			secret = string(plain)
		}
	}

	code, durationMS, sendErr := s.timedPost(ctx, d, secret)
	attempts := d.attempts + 1

	_ = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		if sendErr == nil && code >= 200 && code < 300 {
			_, err := tx.Exec(ctx, `
				UPDATE plugin_hook_deliveries
				   SET status='sent', attempts=$3, response_code=$4,
				       sent_at=now(), error_message='', next_retry_at=NULL,
				       last_duration_ms=$5
				 WHERE tenant_id=$1 AND id=$2`, tenantID, d.id, attempts, code, durationMS)
			return err
		}

		msg := ""
		if sendErr != nil {
			msg = sendErr.Error()
		} else {
			msg = "对方返回 HTTP " + strconv.Itoa(code)
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}

		// 4xx 不重试：对方明确说这个请求它不要，重发五遍还是不要，
		// 只是把日志和对方的错误率一起刷高。
		terminal := attempts >= d.maxTries || (code >= 400 && code < 500)
		if terminal {
			_, err := tx.Exec(ctx, `
				UPDATE plugin_hook_deliveries
				   SET status='failed', attempts=$3, response_code=nullif($4,0),
				       error_message=$5, next_retry_at=NULL, last_duration_ms=$6
				 WHERE tenant_id=$1 AND id=$2`, tenantID, d.id, attempts, code, msg, durationMS)
			return err
		}
		// 指数退避：1、2、4、8 分钟。对方在重启的话，密集重试帮不上忙。
		backoff := time.Duration(1<<uint(attempts-1)) * time.Minute
		if backoff > 30*time.Minute {
			backoff = 30 * time.Minute
		}
		_, err := tx.Exec(ctx, `
			UPDATE plugin_hook_deliveries
			   SET attempts=$3, response_code=nullif($4,0), error_message=$5,
			       next_retry_at=now() + $6::interval, last_duration_ms=$7
			 WHERE tenant_id=$1 AND id=$2`,
			tenantID, d.id, attempts, code, msg,
			strconv.Itoa(int(backoff.Seconds()))+" seconds", durationMS)
		return err
	})
}

func (s *Service) post(ctx context.Context, d dueDelivery, secret string) (int, error) {
	// 发送前再校验一次地址：钩子可能是在 devMode 下配的，
	// 也可能域名的解析结果变了。
	if err := s.validateEndpoint(d.endpoint); err != nil {
		return 0, err
	}
	return s.send(ctx, d, secret)
}

// timedPost 与 post 相同，另外量出这次尝试的往返耗时（毫秒）。
//
// 只量真正发出去的请求：地址校验没过时请求根本没发，耗时为 nil，
// 落库为 NULL —— 记一个 0 会被读成「对方秒回」。连接失败、超时这类
// 已经发起的尝试照记，它们的耗时正是排查时要看的东西。
func (s *Service) timedPost(ctx context.Context, d dueDelivery, secret string) (int, *int, error) {
	if err := s.validateEndpoint(d.endpoint); err != nil {
		return 0, nil, err
	}
	start := time.Now()
	code, err := s.send(ctx, d, secret)
	ms := int(time.Since(start).Milliseconds())
	return code, &ms, err
}

// send 发出一次签名请求，不做地址校验（由 post / timedPost 负责）。
func (s *Service) send(ctx context.Context, d dueDelivery, secret string) (int, error) {
	timeout := time.Duration(d.timeoutMS) * time.Millisecond
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, d.endpoint,
		strings.NewReader(string(d.payload)))
	if err != nil {
		return 0, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "PandoraPanel-Hook/1")
	req.Header.Set("X-Pandora-Event", d.event)
	req.Header.Set("X-Pandora-Delivery", d.id)
	req.Header.Set("X-Pandora-Timestamp", ts)
	if secret != "" {
		// 签名覆盖时间戳：只签 body 的话，攻击者录下一个请求就能
		// 无限重放。插件那边应当同时校验签名和时间戳新鲜度。
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts))
		mac.Write([]byte("."))
		mac.Write(d.payload)
		req.Header.Set("X-Pandora-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// StartScanner 起一个后台循环，定期发送到期的投递。
func (s *Service) StartScanner(ctx context.Context, tenantID string, every time.Duration) {
	go func() {
		// 错开启动：服务刚起来时数据库连接池还在预热，
		// 这时候扑上去发一批只会和正常请求抢连接。
		select {
		case <-time.After(20 * time.Second):
		case <-ctx.Done():
			return
		}
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			if _, err := s.Dispatch(ctx, tenantID, 50); err != nil {
				// 扫描器不该因为一次失败就退出：下一轮再来。
				_ = err
			}
			select {
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Deliveries 返回某个钩子最近的投递记录，供后台排查。
// duration_ms 是最后一次尝试的往返耗时，00081 之前的记录与没发出去的尝试为 null。
func (s *Service) Deliveries(ctx context.Context, tenantID, hookCode string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []map[string]any
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT d.event, d.status, d.attempts, coalesce(d.response_code,0),
			       d.error_message, to_char(d.created_at,'YYYY-MM-DD HH24:MI:SS'),
			       coalesce(to_char(d.sent_at,'YYYY-MM-DD HH24:MI:SS'),''),
			       d.last_duration_ms
			  FROM plugin_hook_deliveries d
			  JOIN plugin_hooks h ON h.tenant_id=d.tenant_id AND h.id=d.hook_id
			 WHERE d.tenant_id=$1 AND h.code=$2
			 ORDER BY d.created_at DESC LIMIT $3`, tenantID, hookCode, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ev, st, em, ct, sa string
			var at, rc int
			var dur *int
			if err := rows.Scan(&ev, &st, &at, &rc, &em, &ct, &sa, &dur); err != nil {
				return err
			}
			out = append(out, map[string]any{
				"event": ev, "status": st, "attempts": at, "response_code": rc,
				"error_message": em, "created_at": ct, "sent_at": sa, "duration_ms": dur,
			})
		}
		return rows.Err()
	})
	return out, err
}

// TestHook 立刻往钩子发一条测试事件，同步返回状态码与往返耗时（毫秒）。
// 测试投递不落库，耗时只在响应里返回。
func (s *Service) TestHook(ctx context.Context, tenantID, code string) (int, int, error) {
	var d dueDelivery
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT id, endpoint_url, timeout_ms, code, secret_encrypted
			  FROM plugin_hooks WHERE tenant_id=$1 AND code=$2`, tenantID, code).
			Scan(&d.hookID, &d.endpoint, &d.timeoutMS, &d.code, &d.sealed)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, httpx.New(httpx.CodeNotFound, "插件不存在")
	}
	if err != nil {
		return 0, 0, err
	}

	d.id = "test-" + d.hookID
	d.event = "panel.test"
	d.payload = []byte(fmt.Sprintf(
		`{"event":"panel.test","message":"来自潘多拉面板的测试事件","sent_at":%q}`,
		time.Now().UTC().Format(time.RFC3339)))

	secret := ""
	if len(d.sealed) > 0 {
		if plain, err := s.envelope.Open(d.sealed, []byte("plugin_hook:"+d.code)); err == nil {
			secret = string(plain)
		}
	}
	status, durationMS, err := s.timedPost(ctx, d, secret)
	if durationMS == nil {
		return status, 0, err
	}
	return status, *durationMS, err
}
