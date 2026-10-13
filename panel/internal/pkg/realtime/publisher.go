package realtime

import (
	"context"
	"encoding/json"

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

// Publish 对给定频道发布一条事件。
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
	for _, ch := range channels {
		if err := p.rdb.Publish(ctx, ch, body).Err(); err != nil {
			plog.From(ctx).Warn("实时事件发布失败",
				zap.String("channel", ch),
				zap.String("topic", ev.Topic),
				zap.Error(err))
		}
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
	chans := channelsFor(topic, owner)
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
	key := "rt:" + topic + ":" + string(body)
	tx.AfterCommit(ctx, key, func(ctx context.Context) {
		p.Publish(ctx, ev, channels...)
	})
}

// channelsFor 决定一条变更推到哪些频道。
//
// 有归属的变更只推给本人，没有的推给公共（nodes 只推管理端）。
// 这个判断是权限边界：把一条带 user_id 的变更误推成全租户广播，
// 等于告诉所有人「某某刚下了单」。所以宁可判断得保守 ——
// 只要行里有 user_id，就只发给他一个人。
func channelsFor(topic string, owner *uuid.UUID) []string {
	if owner != nil {
		return []string{ChannelUser(*owner), ChannelAdmin()}
	}
	if topic == TopicNodes {
		return []string{ChannelAdmin()}
	}
	return []string{ChannelPublic(), ChannelAdmin()}
}
