package service

import (
	"context"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/testkit"
	"github.com/google/uuid"
)

func newRec(uid uuid.UUID, aud string, now time.Time, epoch string) sessionRecord {
	return sessionRecord{
		ID:        uuid.Must(uuid.NewV7()).String(),
		UserID:    uid,
		Audience:  aud,
		AMR:       []string{"password"},
		CreatedAt: now,
		LastSeen:  now,
		ReauthAt:  now,
		AbsExp:    now.Add(24 * time.Hour),
		UserAgent: "test",
		IPHash:    "",
		RTCur:     hashSecret("s1"),
		Epoch:     epoch,
	}
}

func mustCreate(t *testing.T, st *sessionStore, rec sessionRecord, now time.Time) {
	t.Helper()
	ok, err := st.create(context.Background(), rec, now)
	if err != nil {
		t.Fatalf("create 返回错误: %v", err)
	}
	if !ok {
		t.Fatal("create 应成功")
	}
}

func TestCreateAndCheck(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")

	ok, err := st.create(ctx, rec, now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !ok {
		t.Fatal("create 应返回 true")
	}

	state, chk, err := st.check(ctx, rec.ID, uid, "public", now, 0)
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !chk {
		t.Fatal("check 应 ok")
	}
	if len(state.AMR) != 1 || state.AMR[0] != "password" {
		t.Fatalf("AMR 应为 [password]，得 %v", state.AMR)
	}
	if !state.ReauthAt.Equal(now) {
		t.Fatalf("ReauthAt 应为 %v，得 %v", now, state.ReauthAt)
	}

	_, chk, err = st.check(ctx, rec.ID, uid, "admin", now, 0)
	if err != nil {
		t.Fatalf("check 错误 aud: %v", err)
	}
	if chk {
		t.Fatal("错误 aud 时 check 应 ok=false")
	}

	other := uuid.Must(uuid.NewV7())
	_, chk, err = st.check(ctx, rec.ID, other, "public", now, 0)
	if err != nil {
		t.Fatalf("check 错误 uid: %v", err)
	}
	if chk {
		t.Fatal("错误 uid 时 check 应 ok=false")
	}
}

func TestCreateRejectsChangedEpoch(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())

	if err := rdb.Incr(ctx, epochKey(uid)).Err(); err != nil {
		t.Fatalf("INCR epoch: %v", err)
	}

	rec := newRec(uid, "public", now, "0")
	ok, err := st.create(ctx, rec, now)
	if err != nil {
		t.Fatalf("create epoch 0: %v", err)
	}
	if ok {
		t.Fatal("epoch 已为 1 时用 epoch 0 create 应 false")
	}

	rec2 := newRec(uid, "public", now, "1")
	ok, err = st.create(ctx, rec2, now)
	if err != nil {
		t.Fatalf("create epoch 1: %v", err)
	}
	if !ok {
		t.Fatal("epoch 1 create 应 true")
	}
}

func TestCheckExpiredAndIdle(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())

	expired := newRec(uid, "public", now, "0")
	expired.AbsExp = now.Add(-time.Second)
	mustCreate(t, st, expired, now)

	_, chk, err := st.check(ctx, expired.ID, uid, "public", now, 0)
	if err != nil {
		t.Fatalf("check 过期: %v", err)
	}
	if chk {
		t.Fatal("绝对过期后 check 应 ok=false")
	}
	if n, _ := rdb.Exists(ctx, sessKey(expired.ID)).Result(); n != 0 {
		t.Fatal("过期 check 后 sess 键应已删")
	}
	if rdb.SIsMember(ctx, userSessKey(uid), expired.ID).Val() {
		t.Fatal("过期 check 后 usess 不应再含 sid")
	}

	idleRec := newRec(uid, "admin", now, "0")
	mustCreate(t, st, idleRec, now)
	idle := 2 * time.Hour

	_, chk, err = st.check(ctx, idleRec.ID, uid, "admin", now.Add(idle), idle)
	if err != nil {
		t.Fatalf("check 空闲超时: %v", err)
	}
	if chk {
		t.Fatal("空闲 2h 后传 now+2h 应 ok=false")
	}

	idleRec2 := newRec(uid, "admin", now, "0")
	mustCreate(t, st, idleRec2, now)
	_, chk, err = st.check(ctx, idleRec2.ID, uid, "admin", now.Add(time.Hour), idle)
	if err != nil {
		t.Fatalf("check 空闲内: %v", err)
	}
	if !chk {
		t.Fatal("空闲 1h 时传 now+1h 应 ok=true")
	}
}

func TestCheckTouchesLastSeenThrottled(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")
	mustCreate(t, st, rec, now)

	_, _, err := st.check(ctx, rec.ID, uid, "public", now.Add(time.Minute), 0)
	if err != nil {
		t.Fatalf("check +1min: %v", err)
	}
	ls, err := rdb.HGet(ctx, sessKey(rec.ID), "last_seen").Int64()
	if err != nil {
		t.Fatalf("HGET last_seen: %v", err)
	}
	if ls != now.Unix() {
		t.Fatalf("+1min check 后 last_seen 应仍为 %d，得 %d", now.Unix(), ls)
	}

	later := now.Add(6 * time.Minute)
	_, _, err = st.check(ctx, rec.ID, uid, "public", later, 0)
	if err != nil {
		t.Fatalf("check +6min: %v", err)
	}
	ls, err = rdb.HGet(ctx, sessKey(rec.ID), "last_seen").Int64()
	if err != nil {
		t.Fatalf("HGET last_seen: %v", err)
	}
	if ls != later.Unix() {
		t.Fatalf("+6min check 后 last_seen 应为 %d，得 %d", later.Unix(), ls)
	}
}

func TestRevokeUserBumpsEpoch(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())

	p1 := newRec(uid, "public", now, "0")
	p2 := newRec(uid, "public", now, "0")
	adm := newRec(uid, "admin", now, "0")
	mustCreate(t, st, p1, now)
	mustCreate(t, st, p2, now)
	mustCreate(t, st, adm, now)

	n, err := st.revokeUser(ctx, uid, p1.ID, revokeAll)
	if err != nil {
		t.Fatalf("revokeUser: %v", err)
	}
	if n != 2 {
		t.Fatalf("应删掉 2 个会话，得 %d", n)
	}

	_, chk, err := st.check(ctx, p1.ID, uid, "public", now, 0)
	if err != nil {
		t.Fatalf("check 保留会话: %v", err)
	}
	if !chk {
		t.Fatal("保留的会话 check 应仍 ok")
	}

	_, chk, err = st.check(ctx, p2.ID, uid, "public", now, 0)
	if err != nil {
		t.Fatalf("check 已删: %v", err)
	}
	if chk {
		t.Fatal("被删的 public 会话 check 应失败")
	}
	_, chk, err = st.check(ctx, adm.ID, uid, "admin", now, 0)
	if err != nil || chk {
		t.Fatalf("被删的 admin 会话 check 应失败，ok=%v err=%v", chk, err)
	}

	ep, err := st.epoch(ctx, uid)
	if err != nil {
		t.Fatalf("epoch: %v", err)
	}
	if ep != "1" {
		t.Fatalf("uepoch 应为 1，得 %q", ep)
	}
}

func TestRevokeUserByAudience(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())

	pub := newRec(uid, "public", now, "0")
	adm := newRec(uid, "admin", now, "0")
	mustCreate(t, st, pub, now)
	mustCreate(t, st, adm, now)

	n, err := st.revokeUser(ctx, uid, "", revokePublic)
	if err != nil {
		t.Fatalf("revokePublic: %v", err)
	}
	if n != 1 {
		t.Fatalf("应删 1 个 public，得 %d", n)
	}
	_, chk, err := st.check(ctx, pub.ID, uid, "public", now, 0)
	if err != nil || chk {
		t.Fatalf("public 应失效，ok=%v err=%v", chk, err)
	}
	_, chk, err = st.check(ctx, adm.ID, uid, "admin", now, 0)
	if err != nil || !chk {
		t.Fatal("admin 应仍 ok")
	}

	n, err = st.revokeUser(ctx, uid, "", revokeNone)
	if err != nil {
		t.Fatalf("revokeNone: %v", err)
	}
	if n != 0 {
		t.Fatalf("revokeNone 应返回 0，得 %d", n)
	}
	_, chk, err = st.check(ctx, adm.ID, uid, "admin", now, 0)
	if err != nil || !chk {
		t.Fatal("revokeNone 后 admin 应仍 ok")
	}
	ep, err := st.epoch(ctx, uid)
	if err != nil || ep != "2" {
		t.Fatalf("epoch 应再加一为 2，得 %q err=%v", ep, err)
	}
}

func TestRotateAndGrace(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")
	rec.RTCur = hashSecret("s1")
	mustCreate(t, st, rec, now)

	status, _, err := st.rotate(ctx, rec.ID, uid, "public", now, 0,
		refreshModeRotate, hashSecret("s1"), hashSecret("s2"))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if status != rotateOK {
		t.Fatalf("首次 rotate 应为 rotateOK，得 %d", status)
	}

	status, _, err = st.rotate(ctx, rec.ID, uid, "public", now, 0,
		refreshModeRotate, hashSecret("s1"), hashSecret("s3"))
	if err != nil {
		t.Fatalf("rotate conflict: %v", err)
	}
	if status != rotateConflict {
		t.Fatalf("旧 rt_cur 再 rotate 应为 rotateConflict，得 %d", status)
	}

	rs, found, err := st.readRefresh(ctx, rec.ID)
	if err != nil || !found {
		t.Fatalf("readRefresh: found=%v err=%v", found, err)
	}
	if rs.RTCur != hashSecret("s2") {
		t.Fatalf("RTCur 应为 s2 哈希")
	}
	if rs.RTPrev != hashSecret("s1") {
		t.Fatalf("RTPrev 应为 s1 哈希")
	}
	if !rs.RTRotatedAt.Equal(now) {
		t.Fatalf("RTRotatedAt 应为 %v，得 %v", now, rs.RTRotatedAt)
	}

	status, _, err = st.rotate(ctx, rec.ID, uid, "public", now.Add(10*time.Second), 0,
		refreshModeGrace, hashSecret("s1"), "")
	if err != nil || status != rotateOK {
		t.Fatalf("grace 10s 内应 rotateOK，status=%d err=%v", status, err)
	}
	// 宽限不改 refresh 状态：rt_cur、rt_prev、轮换时间都原样
	if after, _, err := st.readRefresh(ctx, rec.ID); err != nil || after.RTCur != hashSecret("s2") || after.RTPrev != hashSecret("s1") || !after.RTRotatedAt.Equal(now) {
		t.Fatalf("grace 后状态被改了: %+v %v", after, err)
	}

	status, _, err = st.rotate(ctx, rec.ID, uid, "public", now.Add(31*time.Second), 0,
		refreshModeGrace, hashSecret("s1"), "")
	if err != nil || status != rotateConflict {
		t.Fatalf("grace 31s 后应 rotateConflict，status=%d err=%v", status, err)
	}
}

func TestRotateInvalidAfterEpochChange(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")
	mustCreate(t, st, rec, now)

	if err := rdb.Incr(ctx, epochKey(uid)).Err(); err != nil {
		t.Fatalf("INCR epoch: %v", err)
	}

	status, _, err := st.rotate(ctx, rec.ID, uid, "public", now, 0,
		refreshModeRotate, hashSecret("s1"), hashSecret("s2"))
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if status != rotateInvalid {
		t.Fatalf("纪元变更后 rotate 应为 rotateInvalid，得 %d", status)
	}
	if n, _ := rdb.Exists(ctx, sessKey(rec.ID)).Result(); n != 0 {
		t.Fatal("rotateInvalid 后会话应已删除")
	}
}

func TestRevokeOne(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	other := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")
	mustCreate(t, st, rec, now)

	ok, err := st.revokeOne(ctx, rec.ID, other, "")
	if err != nil || ok {
		t.Fatal("别的 uid 撤应 false")
	}

	ok, err = st.revokeOne(ctx, rec.ID, uid, "admin")
	if err != nil || ok {
		t.Fatal("aud 不符撤应 false")
	}

	ok, err = st.revokeOne(ctx, rec.ID, uid, "public")
	if err != nil || !ok {
		t.Fatalf("正确撤应 true，ok=%v err=%v", ok, err)
	}

	ok, err = st.revokeOne(ctx, rec.ID, uid, "public")
	if err != nil || ok {
		t.Fatal("再撤应 false")
	}
}

func TestStampReauth(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "admin", now, "0")
	mustCreate(t, st, rec, now)
	idle := 2 * time.Hour

	at := now.Add(5 * time.Minute)
	ok, err := st.stampReauth(ctx, rec.ID, uid, "admin", at, idle)
	if err != nil || !ok {
		t.Fatalf("stampReauth 应 true，ok=%v err=%v", ok, err)
	}
	state, chk, err := st.check(ctx, rec.ID, uid, "admin", at, idle)
	if err != nil || !chk {
		t.Fatal("check 应 ok")
	}
	if !state.ReauthAt.Equal(at) {
		t.Fatalf("ReauthAt 应为 %v，得 %v", at, state.ReauthAt)
	}

	if ok, err = st.stampReauth(ctx, "missing-sid", uid, "admin", at, idle); err != nil || ok {
		t.Fatal("不存在 sid stampReauth 应 false")
	}
	if ok, err = st.stampReauth(ctx, rec.ID, uid, "public", at, idle); err != nil || ok {
		t.Fatal("域不符 stampReauth 应 false")
	}
	// 后台空闲超时后不再盖章
	later := at.Add(idle)
	if ok, err = st.stampReauth(ctx, rec.ID, uid, "admin", later, idle); err != nil || ok {
		t.Fatal("空闲超时后 stampReauth 应 false")
	}
	// 纪元推进（改密）后不再盖章
	rec2 := newRec(uid, "admin", now, "0")
	mustCreate(t, st, rec2, now)
	if err := rdb.Incr(ctx, epochKey(uid)).Err(); err != nil {
		t.Fatal(err)
	}
	if ok, err = st.stampReauth(ctx, rec2.ID, uid, "admin", at, idle); err != nil || ok {
		t.Fatal("纪元变了 stampReauth 应 false")
	}
}

func TestCheckReturnsSessionAudience(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	adm := newRec(uid, "admin", now, "0")
	pub := newRec(uid, "public", now, "0")
	mustCreate(t, st, adm, now)
	mustCreate(t, st, pub, now)

	state, ok, err := st.check(ctx, adm.ID, uid, "", now, time.Hour)
	if err != nil || !ok || state.Audience != "admin" {
		t.Fatalf("不比较域时应带回 admin：ok=%v aud=%q err=%v", ok, state.Audience, err)
	}
	// 空闲只作用于后台会话：门户会话两小时不动仍有效
	state, ok, err = st.check(ctx, pub.ID, uid, "", now.Add(2*time.Hour), time.Hour)
	if err != nil || !ok || state.Audience != "public" {
		t.Fatalf("门户会话不受后台空闲限制：ok=%v aud=%q err=%v", ok, state.Audience, err)
	}
}

func TestListFiltersAndSorts(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())

	older := newRec(uid, "public", now, "0")
	newer := newRec(uid, "public", now, "0")
	adm := newRec(uid, "admin", now, "0")
	expired := newRec(uid, "public", now, "0")
	expired.AbsExp = now.Add(-time.Hour)
	mustCreate(t, st, older, now)
	mustCreate(t, st, newer, now)
	mustCreate(t, st, adm, now)
	mustCreate(t, st, expired, now)

	_, _, _ = st.check(ctx, older.ID, uid, "public", now.Add(6*time.Minute), 0)
	_, _, _ = st.check(ctx, newer.ID, uid, "public", now.Add(12*time.Minute), 0)

	ghost := uuid.Must(uuid.NewV7()).String()
	if err := rdb.SAdd(ctx, userSessKey(uid), ghost).Err(); err != nil {
		t.Fatalf("SADD ghost: %v", err)
	}

	list, err := st.list(ctx, uid, "public", now)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list 应 2 个 public，得 %d", len(list))
	}
	if list[0].ID != newer.ID || list[1].ID != older.ID {
		t.Fatalf("应按 LastSeen 降序，得 %s 然后 %s", list[0].ID, list[1].ID)
	}
	if rdb.SIsMember(ctx, userSessKey(uid), ghost).Val() {
		t.Fatal("幽灵 sid 应从 usess 移除")
	}
}

func TestStoreQuickLogin(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := uuid.Must(uuid.NewV7())
	rec := newRec(uid, "public", now, "0")
	mustCreate(t, st, rec, now)

	ok, err := st.issueQuickLogin(ctx, rec.ID, uid, hashSecret("q1"), 60*time.Second)
	if err != nil || !ok {
		t.Fatalf("issue q1: ok=%v err=%v", ok, err)
	}

	ok, err = st.issueQuickLogin(ctx, rec.ID, uid, hashSecret("q2"), 60*time.Second)
	if err != nil || !ok {
		t.Fatalf("issue q2: %v", err)
	}
	if n, _ := rdb.Exists(ctx, quickLoginKey(hashSecret("q1"))).Result(); n != 0 {
		t.Fatal("签 q2 后 q1 的 ql 键应已删")
	}

	sid, gotUID, ok, err := st.consumeQuickLogin(ctx, hashSecret("q2"), "public", now)
	if err != nil || !ok || sid != rec.ID || gotUID != uid {
		t.Fatalf("消费 q2: ok=%v sid=%s uid=%v err=%v", ok, sid, gotUID, err)
	}

	_, _, ok, err = st.consumeQuickLogin(ctx, hashSecret("q2"), "public", now)
	if err != nil || ok {
		t.Fatal("再次消费应 ok=false")
	}

	ok, err = st.issueQuickLogin(ctx, "no-such-session", uid, hashSecret("qx"), 60*time.Second)
	if err != nil || ok {
		t.Fatal("不存在源会话签发应 false")
	}

	rec2 := newRec(uid, "public", now, "0")
	mustCreate(t, st, rec2, now)
	ok, err = st.issueQuickLogin(ctx, rec2.ID, uid, hashSecret("qr"), 60*time.Second)
	if err != nil || !ok {
		t.Fatalf("issue qr: %v", err)
	}
	ok, _ = st.revokeOne(ctx, rec2.ID, uid, "public")
	if !ok {
		t.Fatal("revoke 源会话")
	}
	_, _, ok, err = st.consumeQuickLogin(ctx, hashSecret("qr"), "public", now)
	if err != nil || ok {
		t.Fatal("源会话被 revoke 后令牌消费应 false")
	}
}

func TestMarkOnce(t *testing.T) {
	_, rdb := testkit.Redis(t)
	st := newSessionStore(rdb)
	ctx := context.Background()
	ttl := 10 * time.Minute

	ok, err := st.markOnce(ctx, ttl, "lfa:a", "lfa:b")
	if err != nil || !ok {
		t.Fatalf("首次 markOnce 应 true，ok=%v err=%v", ok, err)
	}

	ok, err = st.markOnce(ctx, ttl, "lfa:a", "lfa:c")
	if err != nil || ok {
		t.Fatal("a 已存在时 markOnce 应 false")
	}

	// 上一轮没成功时什么都不写：c 仍是新的
	ok, err = st.markOnce(ctx, ttl, "lfa:c")
	if err != nil || !ok {
		t.Fatal("上一轮失败不应写 c，单独 c 应 true")
	}
}
