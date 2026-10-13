package service

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/aegispanel/aegis/internal/module/plugin/repo"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

var _ store = (*fakeStore)(nil)

// fakeStore 是内存里的 plugin store。
type fakeStore struct {
	mu sync.Mutex

	hooks      map[uuid.UUID]*model.Hook
	deliveries map[uuid.UUID]*model.Delivery
	order      []uuid.UUID
	calls      []string

	failExhaustedN int
	claimErr       error
	lockBusy       bool // 模拟别的实例正持有派发锁
	lastDelay      time.Duration

	now func() time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		hooks:      make(map[uuid.UUID]*model.Hook),
		deliveries: make(map[uuid.UUID]*model.Delivery),
		now:        time.Now,
	}
}

func (f *fakeStore) appendCall(name string) {
	f.calls = append(f.calls, name)
}

func (f *fakeStore) ListHooks(_ context.Context) ([]model.Hook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Hook, 0, len(f.hooks))
	for _, h := range f.hooks {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

func (f *fakeStore) HookByCode(_ context.Context, code string, _ bool) (model.Hook, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, h := range f.hooks {
		if h.Code == code {
			return *h, true, nil
		}
	}
	return model.Hook{}, false, nil
}

func (f *fakeStore) HooksByIDs(_ context.Context, ids []uuid.UUID) ([]model.Hook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]model.Hook, 0, len(ids))
	for _, id := range ids {
		if h, ok := f.hooks[id]; ok {
			out = append(out, *h)
		}
	}
	return out, nil
}

func (f *fakeStore) InsertHook(_ context.Context, h *model.Hook) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.hooks {
		if existing.Code == h.Code {
			return repo.ErrDuplicate
		}
	}
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	cp := *h
	f.hooks[h.ID] = &cp
	return nil
}

func (f *fakeStore) UpdateHook(_ context.Context, h *model.Hook, withSecret bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	old, ok := f.hooks[h.ID]
	if !ok {
		return nil
	}
	cp := *h
	if !withSecret {
		cp.SecretEnc = old.SecretEnc
	}
	f.hooks[h.ID] = &cp
	return nil
}

func (f *fakeStore) DeleteHook(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.hooks, id)
	for did, d := range f.deliveries {
		if d.HookID == id {
			delete(f.deliveries, did)
		}
	}
	newOrder := make([]uuid.UUID, 0, len(f.order))
	for _, did := range f.order {
		if f.deliveries[did] != nil {
			newOrder = append(newOrder, did)
		}
	}
	f.order = newOrder
	return nil
}

func (f *fakeStore) HookStats(_ context.Context, since time.Time) ([]repo.HookStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]repo.HookStat, 0, len(f.hooks))
	for id := range f.hooks {
		st := repo.HookStat{HookID: id}
		var lastSent *time.Time
		for _, d := range f.deliveries {
			if d.HookID != id {
				continue
			}
			switch d.Status {
			case model.StatusQueued:
				st.Queued++
			case model.StatusFailed:
				if d.CreatedAt.After(since) {
					st.Failed++
				}
			case model.StatusSent:
				if d.CreatedAt.After(since) {
					st.Sent++
				}
				if d.SentAt != nil {
					if lastSent == nil || d.SentAt.After(*lastSent) {
						lastSent = d.SentAt
					}
				}
			}
		}
		st.LastSentAt = lastSent
		out = append(out, st)
	}
	return out, nil
}

func (f *fakeStore) EnqueueEvent(_ context.Context, event, dedupeKey, payload string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	var n int64
	for _, h := range f.hooks {
		if !h.Enabled {
			continue
		}
		var evs []string
		if err := json.Unmarshal([]byte(h.Events), &evs); err != nil {
			continue
		}
		if !containsString(evs, event) {
			continue
		}
		if f.hasDedupe(h.ID, dedupeKey) {
			continue
		}
		d := &model.Delivery{
			ID:          uuid.New(),
			HookID:      h.ID,
			Event:       event,
			DedupeKey:   dedupeKey,
			Payload:     payload,
			Status:      model.StatusQueued,
			Attempts:    0,
			MaxAttempts: h.MaxAttempts,
			NextRetryAt: &now,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		f.deliveries[d.ID] = d
		f.order = append(f.order, d.ID)
		n++
	}
	return n, nil
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func (f *fakeStore) hasDedupe(hookID uuid.UUID, key string) bool {
	for _, d := range f.deliveries {
		if d.HookID == hookID && d.DedupeKey == key {
			return true
		}
	}
	return false
}

func (f *fakeStore) ClaimDue(_ context.Context, limit int, lease time.Duration) ([]model.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCall("ClaimDue")
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	now := f.now()
	out := make([]model.Delivery, 0, limit)
	for _, id := range f.order {
		if len(out) >= limit {
			break
		}
		d := f.deliveries[id]
		if d == nil || d.Status != model.StatusQueued {
			continue
		}
		if d.Attempts >= d.MaxAttempts {
			continue
		}
		if d.NextRetryAt != nil && d.NextRetryAt.After(now) {
			continue
		}
		h, ok := f.hooks[d.HookID]
		if !ok || !h.Enabled {
			continue
		}
		d.Attempts++
		next := now.Add(lease)
		d.NextRetryAt = &next
		d.UpdatedAt = now
		out = append(out, *d)
	}
	return out, nil
}

func (f *fakeStore) FailExhausted(_ context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCall("FailExhausted")
	return f.failExhaustedN, nil
}

func (f *fakeStore) MarkSent(_ context.Context, id uuid.UUID, code int, durationMS *int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCall("MarkSent")
	d, ok := f.deliveries[id]
	if !ok || d.Status != model.StatusQueued {
		return false, nil
	}
	now := f.now()
	d.Status = model.StatusSent
	d.SentAt = &now
	d.NextRetryAt = nil
	d.ResponseCode = &code
	d.ErrorMessage = ""
	d.LastDurationMS = durationMS
	d.UpdatedAt = now
	return true, nil
}

func (f *fakeStore) MarkRetry(_ context.Context, id uuid.UUID, delay time.Duration, code *int, msg string, durationMS *int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCall("MarkRetry")
	d, ok := f.deliveries[id]
	if !ok || d.Status != model.StatusQueued {
		return false, nil
	}
	now := f.now()
	d.NextRetryAt = ptrTime(now.Add(delay))
	d.ResponseCode = code
	d.ErrorMessage = msg
	d.LastDurationMS = durationMS
	d.UpdatedAt = now
	f.lastDelay = delay
	return true, nil
}

func (f *fakeStore) MarkFailed(_ context.Context, id uuid.UUID, code *int, msg string, durationMS *int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.appendCall("MarkFailed")
	d, ok := f.deliveries[id]
	if !ok || d.Status != model.StatusQueued {
		return false, nil
	}
	now := f.now()
	d.Status = model.StatusFailed
	d.NextRetryAt = nil
	d.ResponseCode = code
	d.ErrorMessage = msg
	d.LastDurationMS = durationMS
	d.UpdatedAt = now
	return true, nil
}

func (f *fakeStore) DeliveriesByHook(_ context.Context, hookID uuid.UUID, limit int) ([]model.Delivery, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var rows []model.Delivery
	for _, d := range f.deliveries {
		if d.HookID == hookID {
			rows = append(rows, *d)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].CreatedAt.Equal(rows[j].CreatedAt) {
			return rows[i].CreatedAt.After(rows[j].CreatedAt)
		}
		return bytes.Compare(rows[i].ID[:], rows[j].ID[:]) > 0
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (f *fakeStore) PurgeDeliveries(_ context.Context, before time.Time, batch int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var candidates []uuid.UUID
	for id, d := range f.deliveries {
		if d.CreatedAt.Before(before) {
			candidates = append(candidates, id)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := f.deliveries[candidates[i]], f.deliveries[candidates[j]]
		return di.CreatedAt.Before(dj.CreatedAt)
	})
	if batch > 0 && len(candidates) > batch {
		candidates = candidates[:batch]
	}
	for _, id := range candidates {
		delete(f.deliveries, id)
	}
	newOrder := make([]uuid.UUID, 0, len(f.order))
	for _, id := range f.order {
		if f.deliveries[id] != nil {
			newOrder = append(newOrder, id)
		}
	}
	f.order = newOrder
	return len(candidates), nil
}

// WithDispatchLock 不进 calls：派发用例断言 calls[0] 是 FailExhausted。
func (f *fakeStore) WithDispatchLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error) {
	f.mu.Lock()
	busy := f.lockBusy
	f.mu.Unlock()
	if busy {
		return false, nil
	}
	return true, fn(ctx)
}

func ptrTime(t time.Time) *time.Time { return &t }

// fakeTx 直接执行 fn。
type fakeTx struct{}

func (fakeTx) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

// fakeAudit 记下写过的审计。
type fakeAudit struct {
	events []auditsvc.Event
	err    error
}

func (f *fakeAudit) Write(_ context.Context, e auditsvc.Event) error {
	if f.err != nil {
		return f.err
	}
	f.events = append(f.events, e)
	return nil
}

func testSealer(t *testing.T) *crypto.Sealer {
	t.Helper()
	sl, err := crypto.NewSealer(bytes.Repeat([]byte{7}, 32), 1)
	if err != nil {
		t.Fatal(err)
	}
	return sl
}

func newTestService(t *testing.T, st *fakeStore, allowPrivate bool) (*Service, *fakeAudit) {
	t.Helper()
	audit := &fakeAudit{}
	svc := newService(st, fakeTx{}, audit, testSealer(t), nil, Options{AllowPrivate: allowPrivate})
	return svc, audit
}

func addHook(t *testing.T, s *Service, st *fakeStore, code, endpoint, secret string, events ...string) *model.Hook {
	t.Helper()
	enc, err := s.sealer.Seal([]byte(secret), secretAAD(code))
	if err != nil {
		t.Fatal(err)
	}
	evJSON, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	h := &model.Hook{
		Code:        code,
		Name:        code,
		Enabled:     true,
		Events:      string(evJSON),
		EndpointURL: endpoint,
		SecretEnc:   enc,
		TimeoutMS:   2000,
		MaxAttempts: 5,
	}
	if err := st.InsertHook(context.Background(), h); err != nil {
		t.Fatal(err)
	}
	return h
}
