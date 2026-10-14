package realtime

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Publisher 经 Redis PUBLISH 广播事件；serve 与 worker 共用。
type Publisher struct {
	rdb *redis.Client
}

// NewPublisher 构造 Publisher；rdb 为 nil 时所有方法都是空操作。
func NewPublisher(rdb *redis.Client) *Publisher {
	return &Publisher{rdb: rdb}
}

// publishTimeout 是一次发布的上限：发布在事务提交后同步做，Redis 抖动时不能把已提交的请求拖住。
const publishTimeout = 500 * time.Millisecond

// Publish 对给定频道发布一条事件，多个频道一次往返发完。
//
// 失败只记日志不返回错误：推送是锦上添花，数据已经写进数据库了。
// 让一次推送失败回滚业务操作，是本末倒置。
func (p *Publisher) Publish(ctx context.Context, ev Event, channels ...string) {
	if p == nil || p.rdb == nil || len(channels) == 0 {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	pipe := p.rdb.Pipeline()
	for _, ch := range channels {
		pipe.Publish(ctx, ch, body)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		plog.From(ctx).Warn("实时事件发布失败",
			zap.Strings("channels", channels),
			zap.String("topic", ev.Topic),
			zap.Error(err))
	}
}

// Changed 在事务提交后推送「某行变了」类事件。
func (p *Publisher) Changed(ctx context.Context, topic, table, op string, id uuid.UUID, owner *uuid.UUID) {
	if p == nil {
		return
	}
	ev := Event{
		Topic: topic,
		Payload: map[string]any{
			"table": table,
			"op":    op,
			"id":    id.String(),
		},
	}
	chans := channelsFor(ctx, topic, owner)
	key := "rt:" + topic + ":" + id.String()
	tx.AfterCommit(ctx, key, func(ctx context.Context) {
		p.Publish(ctx, ev, chans...)
	})
}

// Notify 在事务提交后推送非「某行变了」类事件（如 ticket.updated、switches.changed）。
func (p *Publisher) Notify(ctx context.Context, topic string, payload map[string]any, channels ...string) {
	if p == nil {
		return
	}
	ev := Event{Topic: topic, Payload: payload}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	// 去重键带上频道：同一事务里同一载荷发给不同频道是两件事
	key := "rt:" + topic + ":" + string(body) + ":" + strings.Join(channels, ",")
	tx.AfterCommit(ctx, key, func(ctx context.Context) {
		p.Publish(ctx, ev, channels...)
	})
}

// channelsFor 决定一条变更推到哪些频道。
//
// 这个判断是权限边界：把一条带 user_id 的变更误推成全站广播，
// 等于告诉所有人「某某刚下了单」。所以失败即关闭：只有明确公开的主题才发公共频道，
// 按用户划分的主题漏传 owner 时只发管理端，并记日志等修调用方。
func channelsFor(ctx context.Context, topic string, owner *uuid.UUID) []string {
	if owner != nil {
		return []string{ChannelUser(*owner), ChannelAdmin()}
	}
	switch topic {
	case TopicPlans, TopicAnnouncements, TopicSwitches:
		return []string{ChannelPublic(), ChannelAdmin()}
	case TopicNodes:
		return []string{ChannelAdmin()}
	default:
		plog.From(ctx).Warn("按用户划分的实时事件缺 owner，只发管理端", zap.String("topic", topic))
		return []string{ChannelAdmin()}
	}
}
