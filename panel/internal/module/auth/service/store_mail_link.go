package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Redis 里的邮件免密登录状态：
//
//	ml:{sha256}  string 邮件登录令牌：<uid>|<epoch>，TTL 15 分钟
//	mlu:{uid}    string 记该用户当前那一条的哈希（同一用户只保留最新一条链接），TTL 15 分钟
//
// 脚本里会拼出不在 KEYS 里的键（ml:、mlu:），只适用于单实例 Redis（部署就是单实例）。

func mailLinkKey(hash string) string       { return "ml:" + hash }
func mailLinkUserKey(uid uuid.UUID) string { return "mlu:" + uid.String() }

// 签发邮件登录链接：取 mlu:{uid} 旧值，有就 DEL ml:旧值；SET ml:hash = uid|epoch EX ttl；SET mlu:uid = hash EX ttl。
var mailLinkIssueScript = redis.NewScript(`
local old = redis.call('GET', KEYS[2])
if old then redis.call('DEL', 'ml:' .. old) end
redis.call('SET', KEYS[1], ARGV[1] .. '|' .. ARGV[2], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[4], 'EX', ARGV[3])
return 1
`)

// issueMailLink 登记邮件登录令牌（hash 是明文的 SHA-256 hex）。
func (st *sessionStore) issueMailLink(ctx context.Context, uid uuid.UUID, hash, epoch string, ttl time.Duration) error {
	keys := []string{mailLinkKey(hash), mailLinkUserKey(uid)}
	_, err := mailLinkIssueScript.Run(ctx, st.rdb, keys, uid.String(), epoch, int64(ttl/time.Second), hash).Int64()
	return err
}

// 消费邮件登录链接：GET ml:hash，没有回 {0}；DEL 它；拆出 uid 与 epoch；mlu:uid 的值等于 hash 时一并 DEL；回 {1, uid, epoch}。
var mailLinkConsumeScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return {0} end
redis.call('DEL', KEYS[1])
local sep = string.find(v, '|', 1, true)
if not sep then return {0} end
local uid = string.sub(v, 1, sep - 1)
local ep = string.sub(v, sep + 1)
local cur = redis.call('GET', 'mlu:' .. uid)
if cur == ARGV[1] then
  redis.call('DEL', 'mlu:' .. uid)
end
return {1, uid, ep}
`)

// consumeMailLink 消费邮件登录令牌，返回用户与签发时的纪元；无效 ok 为 false。
func (st *sessionStore) consumeMailLink(ctx context.Context, hash string) (uuid.UUID, string, bool, error) {
	res, err := mailLinkConsumeScript.Run(ctx, st.rdb, []string{mailLinkKey(hash)}, hash).Slice()
	if err != nil {
		return uuid.Nil, "", false, err
	}
	if len(res) < 3 || toInt64(res[0]) != 1 {
		return uuid.Nil, "", false, nil
	}
	uid, err := uuid.Parse(toString(res[1]))
	if err != nil {
		return uuid.Nil, "", false, nil
	}
	epoch := toString(res[2])
	return uid, epoch, true, nil
}
