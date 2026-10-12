package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Redis 里的会话状态（docs/architecture.md 8.2）：
//
//	sess:{sid}     hash   uid aud amr created_at last_seen reauth_at abs_exp ua ip_hash rt_cur rt_prev rt_rotated_at epoch
//	usess:{uid}    set    该用户的全部 sid（可能含已过期的，读与吊销时顺手清掉）
//	uepoch:{uid}   string 凭据纪元：改密、重置、停用、授予或收回后台角色时 INCR；会话里的 epoch 与它不等即失效
//	ql:{sha256}    string 快捷登录：<源 sid>|<uid>，60 秒；qls:{sid} 记源会话当前那一条的哈希
//	lf:{aud}:{h}   string 登录失败计数（h 是邮箱的 HMAC）；lfa:* 是失败审计的去重标记
//
// 时间一律存 Unix 秒。脚本里会拼出不在 KEYS 里的键（sess:、ql:、uepoch:），只适用于单实例 Redis（部署就是单实例）。

const (
	// lastSeenEvery 是 last_seen 的节流间隔：距上次写超过它才写。
	lastSeenEvery = 5 * time.Minute
	// refreshGrace 内拿上一代 refresh 来的请求只发 access、不轮换。
	refreshGrace = 30 * time.Second
)

func sessKey(sid string) string           { return "sess:" + sid }
func userSessKey(uid uuid.UUID) string    { return "usess:" + uid.String() }
func epochKey(uid uuid.UUID) string       { return "uepoch:" + uid.String() }
func quickLoginKey(hash string) string    { return "ql:" + hash }
func quickLoginSessKey(sid string) string { return "qls:" + sid }

// sessionRecord 是 sess:{sid} 的内容。
type sessionRecord struct {
	ID          string
	UserID      uuid.UUID
	Audience    string
	AMR         []string
	CreatedAt   time.Time
	LastSeen    time.Time
	ReauthAt    time.Time // 零值表示没有
	AbsExp      time.Time
	UserAgent   string
	IPHash      string
	RTCur       string // refresh 随机串的 SHA-256（hex）
	RTPrev      string
	RTRotatedAt time.Time
	Epoch       string
}

// sessionState 是校验通过后从会话里取出的内容。
type sessionState struct {
	AMR      []string
	ReauthAt time.Time
	AbsExp   time.Time
	Audience string // 会话自己的域（只有 check 填）
}

// refreshState 是 refresh 第一步读出的会话字段（比较在 Go 里用常量时间做）。
type refreshState struct {
	UserID      uuid.UUID
	Audience    string
	RTCur       string
	RTPrev      string
	RTRotatedAt time.Time
}

// rotate 的结果。
const (
	rotateInvalid  = 0 // 会话已失效（已删除）
	rotateOK       = 1
	rotateConflict = 2 // 与并发的轮换撞上，重读再判
)

// refresh 的两种模式。
const (
	refreshModeRotate = "rotate"
	refreshModeGrace  = "grace"
)

// revokeUser 的 aud 参数：全部、只门户、只后台、一个都不删（只推进纪元）。
const (
	revokeAll    = "*"
	revokeNone   = "-"
	revokePublic = "public"
	revokeAdmin  = "admin"
)

// sessionStore 封装会话相关的 Redis 读写；每个方法一次往返（list 除外）。
type sessionStore struct {
	rdb *redis.Client
}

func newSessionStore(rdb *redis.Client) *sessionStore {
	return &sessionStore{rdb: rdb}
}

// 建会话：纪元与登录前读到的值不同（期间改过密、被停用）就不建，返回 0。
var createScript = redis.NewScript(`
local ep = redis.call('GET', KEYS[3])
if not ep then ep = '0' end
if ep ~= ARGV[1] then return 0 end
redis.call('HSET', KEYS[1], unpack(ARGV, 5))
redis.call('EXPIREAT', KEYS[1], ARGV[2])
redis.call('SADD', KEYS[2], ARGV[4])
local ttl = redis.call('TTL', KEYS[2])
if ttl < tonumber(ARGV[2]) - tonumber(ARGV[3]) then
  redis.call('EXPIREAT', KEYS[2], ARGV[2])
end
return 1
`)

// create 写入新会话；纪元已变返回 false。
func (st *sessionStore) create(ctx context.Context, rec sessionRecord, now time.Time) (bool, error) {
	args := []any{
		rec.Epoch, rec.AbsExp.Unix(), now.Unix(), rec.ID,
		"uid", rec.UserID.String(),
		"aud", rec.Audience,
		"amr", strings.Join(rec.AMR, ","),
		"created_at", rec.CreatedAt.Unix(),
		"last_seen", rec.LastSeen.Unix(),
		"reauth_at", unixOrZero(rec.ReauthAt),
		"abs_exp", rec.AbsExp.Unix(),
		"ua", rec.UserAgent,
		"ip_hash", rec.IPHash,
		"rt_cur", rec.RTCur,
		"rt_prev", "",
		"rt_rotated_at", 0,
		"epoch", rec.Epoch,
	}
	keys := []string{sessKey(rec.ID), userSessKey(rec.UserID), epochKey(rec.UserID)}
	n, err := createScript.Run(ctx, st.rdb, keys, args...).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// 每个请求的会话校验：uid 对得上（ARGV[2] 非空时 aud 也要对得上）、没过绝对期、后台会话没空闲超时、纪元一致；
// 失效的顺手删掉。通过时按节流更新 last_seen，返回 {1, reauth_at, amr, abs_exp, aud}。
var checkScript = redis.NewScript(`
local h = redis.call('HMGET', KEYS[1], 'uid', 'aud', 'abs_exp', 'last_seen', 'epoch', 'reauth_at', 'amr')
if not h[1] then
  redis.call('SREM', KEYS[2], ARGV[6])
  return {0}
end
if h[1] ~= ARGV[1] or (ARGV[2] ~= '' and h[2] ~= ARGV[2]) then return {0} end
local now = tonumber(ARGV[3])
local idle = 0
if h[2] == 'admin' then idle = tonumber(ARGV[4]) end
local ls = tonumber(h[4]) or 0
local ep = redis.call('GET', KEYS[3]) or '0'
if (tonumber(h[3]) or 0) <= now or (idle > 0 and now - ls >= idle) or ep ~= h[5] then
  redis.call('DEL', KEYS[1])
  redis.call('SREM', KEYS[2], ARGV[6])
  redis.call('PUBLISH', 'rt:sess:' .. ARGV[6], 'revoked')
  return {0}
end
if now - ls >= tonumber(ARGV[5]) then
  redis.call('HSET', KEYS[1], 'last_seen', ARGV[3])
end
return {1, h[6] or '0', h[7] or '', h[3], h[2]}
`)

// check 校验会话；aud 为空表示不比较域（结果里带回会话自己的域）；adminIdle 只作用于后台会话。
// ok 为 false 表示会话无效（不是 Redis 故障）。
func (st *sessionStore) check(ctx context.Context, sid string, uid uuid.UUID, aud string,
	now time.Time, adminIdle time.Duration) (sessionState, bool, error) {
	keys := []string{sessKey(sid), userSessKey(uid), epochKey(uid)}
	res, err := checkScript.Run(ctx, st.rdb, keys,
		uid.String(), aud, now.Unix(), int64(adminIdle/time.Second), int64(lastSeenEvery/time.Second), sid).Slice()
	if err != nil {
		return sessionState{}, false, err
	}
	if len(res) < 5 || toInt64(res[0]) != 1 {
		return sessionState{}, false, nil
	}
	return sessionState{
		ReauthAt: fromUnix(toInt64(res[1])),
		AMR:      splitAMR(toString(res[2])),
		AbsExp:   fromUnix(toInt64(res[3])),
		Audience: toString(res[4]),
	}, true, nil
}

// readRefresh 读 refresh 判定需要的字段；会话不存在 found 为 false。
func (st *sessionStore) readRefresh(ctx context.Context, sid string) (refreshState, bool, error) {
	vals, err := st.rdb.HMGet(ctx, sessKey(sid), "uid", "aud", "rt_cur", "rt_prev", "rt_rotated_at").Result()
	if err != nil {
		return refreshState{}, false, err
	}
	if len(vals) != 5 || vals[0] == nil {
		return refreshState{}, false, nil
	}
	uid, err := uuid.Parse(toString(vals[0]))
	if err != nil {
		return refreshState{}, false, nil
	}
	return refreshState{
		UserID:      uid,
		Audience:    toString(vals[1]),
		RTCur:       toString(vals[2]),
		RTPrev:      toString(vals[3]),
		RTRotatedAt: fromUnix(parseInt64(toString(vals[4]))),
	}, true, nil
}

// refresh 的原子一步：先做与 check 相同的有效性校验，再按模式核对期望值（CAS）。
// rotate：rt_cur 仍等于期望值才轮换（rt_prev ← rt_cur、rt_cur ← 新值）；
// grace：rt_prev 仍等于期望值且轮换不超过宽限才放行。返回 {状态, amr, abs_exp, reauth_at}。
var rotateScript = redis.NewScript(`
local h = redis.call('HMGET', KEYS[1], 'uid', 'aud', 'abs_exp', 'last_seen', 'epoch', 'rt_cur', 'rt_prev', 'amr', 'reauth_at', 'rt_rotated_at')
if not h[1] or h[1] ~= ARGV[1] or h[2] ~= ARGV[2] then return {0} end
local now = tonumber(ARGV[3])
local idle = tonumber(ARGV[4])
local ls = tonumber(h[4]) or 0
local ep = redis.call('GET', KEYS[3]) or '0'
if (tonumber(h[3]) or 0) <= now or (idle > 0 and now - ls >= idle) or ep ~= h[5] then
  redis.call('DEL', KEYS[1])
  redis.call('SREM', KEYS[2], ARGV[5])
  redis.call('PUBLISH', 'rt:sess:' .. ARGV[5], 'revoked')
  return {0}
end
if ARGV[6] == 'rotate' then
  if h[6] ~= ARGV[7] then return {2} end
  redis.call('HSET', KEYS[1], 'rt_prev', h[6], 'rt_cur', ARGV[8], 'rt_rotated_at', ARGV[3], 'last_seen', ARGV[3])
else
  if h[7] ~= ARGV[7] or now - (tonumber(h[10]) or 0) > tonumber(ARGV[9]) then return {2} end
  redis.call('HSET', KEYS[1], 'last_seen', ARGV[3])
end
return {1, h[8] or '', h[3], h[9] or '0'}
`)

// rotate 执行 refresh 的原子一步；newHash 只在 rotate 模式用。
func (st *sessionStore) rotate(ctx context.Context, sid string, uid uuid.UUID, aud string, now time.Time,
	idle time.Duration, mode, expect, newHash string) (int, sessionState, error) {
	keys := []string{sessKey(sid), userSessKey(uid), epochKey(uid)}
	res, err := rotateScript.Run(ctx, st.rdb, keys,
		uid.String(), aud, now.Unix(), int64(idle/time.Second), sid, mode, expect, newHash,
		int64(refreshGrace/time.Second)).Slice()
	if err != nil {
		return rotateInvalid, sessionState{}, err
	}
	if len(res) == 0 {
		return rotateInvalid, sessionState{}, nil
	}
	status := int(toInt64(res[0]))
	if status != rotateOK || len(res) < 4 {
		return status, sessionState{}, nil
	}
	return rotateOK, sessionState{
		AMR:      splitAMR(toString(res[1])),
		AbsExp:   fromUnix(parseInt64(toString(res[2]))),
		ReauthAt: fromUnix(parseInt64(toString(res[3]))),
	}, nil
}

// 吊销一个会话：uid 对得上（aud 非空时也要对得上）才删。
var revokeOneScript = redis.NewScript(`
local h = redis.call('HMGET', KEYS[1], 'uid', 'aud')
if not h[1] or h[1] ~= ARGV[2] then return 0 end
if ARGV[3] ~= '' and h[2] ~= ARGV[3] then return 0 end
redis.call('DEL', KEYS[1])
redis.call('SREM', KEYS[2], ARGV[1])
redis.call('PUBLISH', 'rt:sess:' .. ARGV[1], 'revoked')
return 1
`)

// revokeOne 删除一个会话；aud 为空表示不限域。不存在或不属于该用户返回 false。
func (st *sessionStore) revokeOne(ctx context.Context, sid string, uid uuid.UUID, aud string) (bool, error) {
	n, err := revokeOneScript.Run(ctx, st.rdb, []string{sessKey(sid), userSessKey(uid)},
		sid, uid.String(), aud).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// 吊销一个用户的会话并推进纪元：ARGV[2] 是要删的域（* 全部、- 不删）；except 与不删的会话把 epoch 改成新值继续有效。
var revokeUserScript = redis.NewScript(`
local ep = redis.call('INCR', KEYS[2])
local sids = redis.call('SMEMBERS', KEYS[1])
local n = 0
for _, sid in ipairs(sids) do
  local k = 'sess:' .. sid
  local aud = redis.call('HGET', k, 'aud')
  if not aud then
    redis.call('SREM', KEYS[1], sid)
  elseif sid == ARGV[1] or (ARGV[2] ~= '*' and aud ~= ARGV[2]) then
    redis.call('HSET', k, 'epoch', ep)
  else
    redis.call('DEL', k)
    redis.call('SREM', KEYS[1], sid)
    redis.call('PUBLISH', 'rt:sess:' .. sid, 'revoked')
    n = n + 1
  end
end
return n
`)

// revokeUser 吊销用户在 aud 域的会话（保留 exceptSID）并推进纪元，返回删掉的个数。
func (st *sessionStore) revokeUser(ctx context.Context, uid uuid.UUID, exceptSID, aud string) (int, error) {
	n, err := revokeUserScript.Run(ctx, st.rdb, []string{userSessKey(uid), epochKey(uid)},
		exceptSID, aud).Int64()
	return int(n), err
}

// 重认证：会话有效（与 check 同样的条件：uid、aud、绝对期、后台空闲、纪元）才盖时间戳，否则不写、返回 0。
var stampReauthScript = redis.NewScript(`
local h = redis.call('HMGET', KEYS[1], 'uid', 'aud', 'abs_exp', 'last_seen', 'epoch')
if not h[1] or h[1] ~= ARGV[1] or h[2] ~= ARGV[3] then return 0 end
local now = tonumber(ARGV[2])
local idle = 0
if h[2] == 'admin' then idle = tonumber(ARGV[4]) end
local ls = tonumber(h[4]) or 0
local ep = redis.call('GET', KEYS[2]) or '0'
if (tonumber(h[3]) or 0) <= now or (idle > 0 and now - ls >= idle) or ep ~= h[5] then return 0 end
redis.call('HSET', KEYS[1], 'reauth_at', ARGV[2], 'last_seen', ARGV[2])
return 1
`)

// stampReauth 把有效会话的 reauth_at 改成 now；会话无效返回 false。
func (st *sessionStore) stampReauth(ctx context.Context, sid string, uid uuid.UUID, aud string,
	now time.Time, adminIdle time.Duration) (bool, error) {
	n, err := stampReauthScript.Run(ctx, st.rdb, []string{sessKey(sid), epochKey(uid)},
		uid.String(), now.Unix(), aud, int64(adminIdle/time.Second)).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// epoch 读用户当前的凭据纪元；没有就是 "0"。
func (st *sessionStore) epoch(ctx context.Context, uid uuid.UUID) (string, error) {
	v, err := st.rdb.Get(ctx, epochKey(uid)).Result()
	if errors.Is(err, redis.Nil) {
		return "0", nil
	}
	return v, err
}

// list 返回用户在 aud 域仍有效的会话（按 last_seen 降序），顺手清掉集合里已不存在的 sid。
func (st *sessionStore) list(ctx context.Context, uid uuid.UUID, aud string, now time.Time) ([]sessionRecord, error) {
	sids, err := st.rdb.SMembers(ctx, userSessKey(uid)).Result()
	if err != nil {
		return nil, err
	}
	if len(sids) == 0 {
		return []sessionRecord{}, nil
	}
	pipe := st.rdb.Pipeline()
	cmds := make([]*redis.SliceCmd, len(sids))
	for i, sid := range sids {
		cmds[i] = pipe.HMGet(ctx, sessKey(sid), "uid", "aud", "created_at", "last_seen", "abs_exp", "ua", "epoch")
	}
	epCmd := pipe.Get(ctx, epochKey(uid))
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	ep, err := epCmd.Result()
	if errors.Is(err, redis.Nil) {
		ep = "0"
	} else if err != nil {
		return nil, err
	}
	out := make([]sessionRecord, 0, len(sids))
	var dead []any
	for i, sid := range sids {
		vals, err := cmds[i].Result()
		if err != nil {
			return nil, err
		}
		if len(vals) != 7 || vals[0] == nil {
			dead = append(dead, sid)
			continue
		}
		if toString(vals[0]) != uid.String() || toString(vals[1]) != aud || toString(vals[6]) != ep {
			continue
		}
		absExp := fromUnix(parseInt64(toString(vals[4])))
		if !absExp.After(now) {
			continue
		}
		out = append(out, sessionRecord{
			ID:        sid,
			UserID:    uid,
			Audience:  aud,
			CreatedAt: fromUnix(parseInt64(toString(vals[2]))),
			LastSeen:  fromUnix(parseInt64(toString(vals[3]))),
			AbsExp:    absExp,
			UserAgent: toString(vals[5]),
		})
	}
	if len(dead) > 0 {
		_ = st.rdb.SRem(ctx, userSessKey(uid), dead...).Err() // 只是清理，失败不影响结果
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// 签发快捷登录：源会话必须属于该用户；同一会话只保留最新一条。
var quickLoginIssueScript = redis.NewScript(`
if redis.call('HGET', KEYS[3], 'uid') ~= ARGV[2] then return 0 end
local old = redis.call('GET', KEYS[2])
if old then redis.call('DEL', 'ql:' .. old) end
redis.call('SET', KEYS[1], ARGV[1] .. '|' .. ARGV[2], 'EX', ARGV[3])
redis.call('SET', KEYS[2], ARGV[4], 'EX', ARGV[3])
return 1
`)

// issueQuickLogin 登记快捷登录令牌（hash 是明文的 SHA-256 hex）；源会话不存在返回 false。
func (st *sessionStore) issueQuickLogin(ctx context.Context, sid string, uid uuid.UUID, hash string, ttl time.Duration) (bool, error) {
	keys := []string{quickLoginKey(hash), quickLoginSessKey(sid), sessKey(sid)}
	n, err := quickLoginIssueScript.Run(ctx, st.rdb, keys, sid, uid.String(), int64(ttl/time.Second), hash).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// 消费快捷登录：取出即删；源会话必须仍有效（属于该用户、门户域、未过期、纪元一致）。
var quickLoginConsumeScript = redis.NewScript(`
local v = redis.call('GET', KEYS[1])
if not v then return {0} end
redis.call('DEL', KEYS[1])
local sep = string.find(v, '|', 1, true)
if not sep then return {0} end
local sid = string.sub(v, 1, sep - 1)
local uid = string.sub(v, sep + 1)
local h = redis.call('HMGET', 'sess:' .. sid, 'uid', 'aud', 'abs_exp', 'epoch')
if not h[1] or h[1] ~= uid or h[2] ~= ARGV[2] or (tonumber(h[3]) or 0) <= tonumber(ARGV[1]) then return {0} end
local ep = redis.call('GET', 'uepoch:' .. uid) or '0'
if ep ~= h[4] then return {0} end
redis.call('DEL', 'qls:' .. sid)
return {1, sid, uid}
`)

// consumeQuickLogin 消费快捷登录令牌，返回源会话与用户；无效 ok 为 false。
func (st *sessionStore) consumeQuickLogin(ctx context.Context, hash, aud string, now time.Time) (string, uuid.UUID, bool, error) {
	res, err := quickLoginConsumeScript.Run(ctx, st.rdb, []string{quickLoginKey(hash)}, now.Unix(), aud).Slice()
	if err != nil {
		return "", uuid.Nil, false, err
	}
	if len(res) < 3 || toInt64(res[0]) != 1 {
		return "", uuid.Nil, false, nil
	}
	uid, err := uuid.Parse(toString(res[2]))
	if err != nil {
		return "", uuid.Nil, false, nil
	}
	return toString(res[1]), uid, true, nil
}

// 先占名额再验：INCR；第一次设窗口期；到阈值（ARGV[2] > 0 时）那一次把期限改成暂停时长。
// 返回 {累计次数（含本次）, 剩余秒数}。并发请求各占一个名额，绕不过上限。
var claimScript = redis.NewScript(`
local n = redis.call('INCR', KEYS[1])
if n == 1 then redis.call('EXPIRE', KEYS[1], ARGV[1]) end
local th = tonumber(ARGV[2])
if th > 0 and n == th then redis.call('EXPIRE', KEYS[1], ARGV[3]) end
return {n, redis.call('TTL', KEYS[1])}
`)

// claimAttempt 占一次试错名额，返回窗口内累计次数（含本次）与剩余期限。
// threshold 为 0 时不改期限；否则第 threshold 次把期限改成 pause。
func (st *sessionStore) claimAttempt(ctx context.Context, key string, window time.Duration,
	threshold int, pause time.Duration) (int, time.Duration, error) {
	res, err := claimScript.Run(ctx, st.rdb, []string{key},
		int64(window/time.Second), threshold, int64(pause/time.Second)).Int64Slice()
	if err != nil {
		return 0, 0, err
	}
	if len(res) != 2 {
		return 0, 0, fmt.Errorf("试错计数脚本返回 %d 项", len(res))
	}
	return int(res[0]), time.Duration(res[1]) * time.Second, nil
}

// clearAttempts 成功后清掉试错计数。
func (st *sessionStore) clearAttempts(ctx context.Context, key string) error {
	return st.rdb.Del(ctx, key).Err()
}

// 退名额：键还在才 DECR（过期后不留下负数的新键）。
var releaseScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then return redis.call('DECR', KEYS[1]) end
return 0
`)

// releaseAttempt 退回一次名额（校验没有结论，或口令其实是对的）。
func (st *sessionStore) releaseAttempt(ctx context.Context, key string) error {
	return releaseScript.Run(ctx, st.rdb, []string{key}).Err()
}

// 全部键都不存在时才一起 SET EX 并返回 1；否则返回 0、什么都不写。
var markOnceScript = redis.NewScript(`
for _, k in ipairs(KEYS) do
  if redis.call('EXISTS', k) == 1 then return 0 end
end
for _, k in ipairs(KEYS) do
  redis.call('SET', k, '1', 'EX', ARGV[1])
end
return 1
`)

// markOnce 窗口内第一次（所有键都没出现过）返回 true，并把这些键一起记下。
func (st *sessionStore) markOnce(ctx context.Context, ttl time.Duration, keys ...string) (bool, error) {
	n, err := markOnceScript.Run(ctx, st.rdb, keys, int64(ttl/time.Second)).Int64()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// fromUnix 把 Unix 秒转成时间；0 表示零值。
func fromUnix(sec int64) time.Time {
	if sec <= 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

func splitAMR(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}

func parseInt64(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case string:
		return parseInt64(x)
	default:
		return 0
	}
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}
