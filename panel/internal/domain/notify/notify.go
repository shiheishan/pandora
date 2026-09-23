// Package notify 实现通知：站内信、邮件，以及到期与流量预警。
//
// 设计上把「决定要通知」和「实际送出去」分成两步，中间隔着 deliveries 表：
//
//	Enqueue  写一条待发记录（同步，在业务事务里）
//	Dispatch 后台取出来投递（异步，可重试）
//
// 这样拆的理由是失败模式完全不同。业务操作（下单、续费）不该因为
// SMTP 连不上而回滚 —— 钱已经收了，通知发不出去是另一回事。
// 而投递本身需要重试、退避、去重，那是一套独立的生命周期。
package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// Channel 是投递渠道。
type Channel string

const (
	ChannelInApp Channel = "inapp"
	ChannelEmail Channel = "email"
)

// Sender 把一条渲染好的通知送出去。
//
// 站内信不需要 Sender —— 它写进表就算送到了，用户来拉即可。
// 只有出网的渠道（邮件、Telegram）才需要实现这个接口。
// ErrChannelNotConfigured 表示这个渠道在当前部署里没配置。
//
// 与投递失败区分开：失败要重试并计入失败率，未配置只需要记一笔然后收手。
var ErrChannelNotConfigured = errors.New("渠道未配置")

type Sender interface {
	Channel() Channel
	Send(ctx context.Context, recipient, subject, body string) error
}

type Service struct {
	pool    *db.Pool
	log     *slog.Logger
	senders map[Channel]Sender
	// salt 用于哈希收件人。投递记录会长期保留，
	// 里面存明文邮箱等于又攒了一份用户通讯录。
	salt []byte
}

func New(pool *db.Pool, log *slog.Logger, salt []byte, senders ...Sender) *Service {
	m := make(map[Channel]Sender, len(senders))
	for _, s := range senders {
		if s != nil {
			m[s.Channel()] = s
		}
	}
	return &Service{pool: pool, log: log, senders: m, salt: salt}
}

// Enqueue 排一条通知。
//
// dedupeKey 决定同一件事只会被通知一次：「到期前 3 天提醒」这种
// 由定时任务反复扫描产生的通知，每轮扫描都会命中同一批订阅，
// 没有去重的话用户每分钟收一条。键里带上业务标识与窗口即可，
// 例如 "expiring:<订阅ID>:3d"。
func (s *Service) Enqueue(ctx context.Context, tx pgx.Tx, tenantID, userID,
	code string, vars map[string]string, dedupeKey string) error {

	// 一个 code 通常有多个渠道的模板（站内 + 邮件），逐个排队
	rows, err := tx.Query(ctx, `
		SELECT channel, category FROM notification_templates
		 WHERE tenant_id = $1 AND code = $2 AND status = 'active' AND locale = 'zh-CN'`,
		tenantID, code)
	if err != nil {
		return err
	}
	type target struct{ channel, category string }
	var targets []target
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.channel, &t.category); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		// 没有模板不是错误：可能这个通知只配了站内没配邮件。
		// 但完全没有任何模板通常意味着 code 拼错了，值得留个痕迹。
		s.log.Warn("通知模板缺失", "code", code)
		return nil
	}

	payload := make(map[string]any, len(vars))
	for k, v := range vars {
		payload[k] = v
	}

	for _, t := range targets {
		// 用户关掉的类别不再排队。
		// transactional 不查偏好 —— 表上的约束已经保证它关不掉，
		// 这里再查一次只是白费一个来回。
		if t.category != "transactional" {
			var enabled bool
			err := tx.QueryRow(ctx, `
				SELECT COALESCE(
					(SELECT enabled FROM notification_preferences
					  WHERE tenant_id = $1 AND user_id = $2::uuid
					    AND category = $3 AND channel = $4), true)`,
				tenantID, userID, t.category, t.channel).Scan(&enabled)
			if err == nil && !enabled {
				continue
			}
		}

		key := dedupeKey
		if key != "" {
			key = key + ":" + t.channel
		}

		// dedupe_key 上有唯一约束时，重复排队会撞键。
		// 用 ON CONFLICT DO NOTHING 让重复变成静默跳过 ——
		// 这正是去重想要的行为，不该报错。
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_deliveries
				(tenant_id, user_id, template_code, channel, dedupe_key,
				 recipient_hash, payload, status, max_attempts, next_retry_at)
			VALUES ($1,$2::uuid,$3,$4,NULLIF($5,''),$6,$7,'queued',5,now())
			ON CONFLICT DO NOTHING`,
			tenantID, userID, code, t.channel, key,
			s.hash(userID), payload); err != nil {
			return fmt.Errorf("排队通知 %s/%s: %w", code, t.channel, err)
		}
	}
	return nil
}

// Render 用变量填充模板。
//
// 只做直白的字符串替换。模板里出现循环或条件，说明这条通知
// 想表达的东西太多了，那该拆成两条，而不是给模板加语法。
func Render(tpl string, vars map[string]any) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", fmt.Sprint(v))
	}
	return out
}

func (s *Service) hash(v string) []byte {
	m := hmac.New(sha256.New, s.salt)
	m.Write([]byte(v))
	return m.Sum(nil)
}

// Dispatch 处理一批待投递的通知。返回处理条数。
//
// 站内信在这里直接标记为已送达：它的「送达」就是写进表，
// 用户拉收件箱时自然会看到。
func (s *Service) Dispatch(ctx context.Context, tenantID string, limit int) (int, error) {
	type job struct {
		id       string
		userID   string
		code     string
		channel  string
		payload  map[string]any
		attempts int
		maxTry   int
	}
	var jobs []job

	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// FOR UPDATE SKIP LOCKED：多个实例可以同时跑派发而不会互相抢同一条。
		// 没有它的话要么加分布式锁（多一个依赖），要么只能单实例跑。
		rows, err := tx.Query(ctx, `
			SELECT id, user_id::text, template_code, channel,
			       COALESCE(payload,'{}'::jsonb), attempts, max_attempts
			  FROM notification_deliveries
			 WHERE tenant_id = $1 AND status = 'queued'
			   AND (next_retry_at IS NULL OR next_retry_at <= now())
			 ORDER BY created_at
			 LIMIT $2
			 FOR UPDATE SKIP LOCKED`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var j job
			if err := rows.Scan(&j.id, &j.userID, &j.code, &j.channel,
				&j.payload, &j.attempts, &j.maxTry); err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return rows.Err()
	})
	if err != nil || len(jobs) == 0 {
		return 0, err
	}

	done := 0
	for _, j := range jobs {
		if err := s.deliver(ctx, tenantID, j.id, j.userID, j.code,
			j.channel, j.payload, j.attempts, j.maxTry); err != nil {
			s.log.Warn("通知投递失败", "id", j.id, "channel", j.channel, "err", err)
		}
		done++
	}
	return done, nil
}

func (s *Service) deliver(ctx context.Context, tenantID, id, userID, code,
	channel string, payload map[string]any, attempts, maxTry int) error {

	// 站内信不出网：写表即送达
	if Channel(channel) == ChannelInApp {
		return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE notification_deliveries
				   SET status = 'sent', sent_at = now(), attempts = attempts + 1
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
			return err
		})
	}

	sender, ok := s.senders[Channel(channel)]
	if !ok {
		// 渠道没配（比如没填 SMTP）。标记为 suppressed 而不是 failed：
		// 这不是投递失败，是这个部署根本没启用这个渠道，
		// 重试多少次都一样，混进失败率里只会掩盖真正的故障。
		return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE notification_deliveries
				   SET status = 'suppressed', error_message = '渠道未配置'
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
			return err
		})
	}

	// 取收件地址与模板。
	//
	// 「收件地址」按渠道解释：邮件是邮箱，Telegram 是 chat_id。
	// 没绑 Telegram 的用户查出来是空串，下面的空值分支会把这条
	// 标成 suppressed —— 那不是投递失败，是这个人没开这个渠道。
	var recipient, subjectTpl, bodyTpl string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		recipientSQL := `SELECT COALESCE(email,'') FROM users
		                  WHERE tenant_id = $1 AND id = $2::uuid`
		if Channel(channel) == ChannelTelegram {
			recipientSQL = `SELECT COALESCE(max(chat_id)::text,'')
			                  FROM telegram_bindings
			                 WHERE tenant_id = $1 AND user_id = $2::uuid`
		}
		if err := tx.QueryRow(ctx, recipientSQL,
			tenantID, userID).Scan(&recipient); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `
			SELECT COALESCE(subject,''), body FROM notification_templates
			 WHERE tenant_id = $1 AND code = $2 AND channel = $3
			   AND status = 'active' AND locale = 'zh-CN'
			 ORDER BY version DESC LIMIT 1`,
			tenantID, code, channel).Scan(&subjectTpl, &bodyTpl)
	})
	if err != nil {
		return err
	}
	if recipient == "" {
		// 用户没绑这个渠道（最常见的是没绑 Telegram）。
		// 这不是投递失败：重试五次结果一样，而记成 failed 会让
		// 失败率里全是「他本来就没开这个渠道」，真正的故障反而看不见。
		if Channel(channel) == ChannelTelegram {
			return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `
					UPDATE notification_deliveries
					   SET status = 'suppressed', error_message = '用户未绑定 Telegram'
					 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
				return err
			})
		}
		// 邮箱为空则是真的异常：注册流程保证每个用户都有邮箱。
		return s.markFailed(ctx, tenantID, id, attempts, maxTry, "收件地址为空")
	}

	sendErr := sender.Send(ctx, recipient, Render(subjectTpl, payload), Render(bodyTpl, payload))
	if errors.Is(sendErr, ErrChannelNotConfigured) {
		// 配置是空的（后台还没填 SMTP）。这不是投递失败，重试多少次都一样，
		// 混进失败率里只会掩盖真正的故障
		return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `
				UPDATE notification_deliveries
				   SET status = 'suppressed', error_message = '渠道未配置'
				 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
			return err
		})
	}
	if sendErr != nil {
		return s.markFailed(ctx, tenantID, id, attempts, maxTry, sendErr.Error())
	}

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE notification_deliveries
			   SET status = 'sent', sent_at = now(), attempts = attempts + 1, error_message = NULL
			 WHERE tenant_id = $1 AND id = $2::uuid`, tenantID, id)
		return err
	})
}

// markFailed 记一次失败，并安排下次重试。
func (s *Service) markFailed(ctx context.Context, tenantID, id string,
	attempts, maxTry int, msg string) error {

	next := attempts + 1
	status := "queued"
	if next >= maxTry {
		status = "failed"
	}
	// 指数退避：1、2、4、8… 分钟。对端故障通常要一段时间才恢复，
	// 固定间隔重试只会在它最脆弱的时候持续加压。
	backoff := time.Duration(1<<uint(min(next, 6))) * time.Minute

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE notification_deliveries
			   SET status = $3, attempts = attempts + 1,
			       error_message = left($4, 500), next_retry_at = now() + $5::interval
			 WHERE tenant_id = $1 AND id = $2::uuid`,
			tenantID, id, status, msg, fmt.Sprintf("%d seconds", int(backoff.Seconds())))
		return err
	})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
