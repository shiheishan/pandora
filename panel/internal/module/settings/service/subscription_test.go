package service

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/google/uuid"
)

// 缺行：前缀生成一次并落库，之后读到的都是同一个；上限取缺省 60；生成不写审计。
func TestSubscriptionDeliveryGeneratesPrefixOnce(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ctx := context.Background()
	first, err := s.SubscriptionDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !subscriptionPathPrefixRe.MatchString(first.PathPrefix) {
		t.Fatalf("前缀 %q 不是 12 位十六进制", first.PathPrefix)
	}
	if first.FetchLimitPerHour != 60 {
		t.Fatalf("缺省上限 %d", first.FetchLimitPerHour)
	}
	var stored string
	if err := json.Unmarshal(st.kv[keySubscriptionPathPrefix], &stored); err != nil || stored != first.PathPrefix {
		t.Fatalf("落库 %q err=%v", stored, err)
	}
	second, err := s.SubscriptionDelivery(ctx)
	if err != nil || second.PathPrefix != first.PathPrefix {
		t.Fatalf("第二次 %q err=%v", second.PathPrefix, err)
	}
	if len(al.entries) != 0 {
		t.Fatalf("生成前缀不该写审计: %+v", al.entries)
	}
}

// 已有前缀与上限时原样读出，不重新生成。
func TestSubscriptionDeliveryReadsExisting(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{
		keySubscriptionPathPrefix: json.RawMessage(`"a1b2c3d4e5f6"`),
		keySubscriptionFetchLimit: json.RawMessage(`5`),
	}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	got, err := s.SubscriptionDelivery(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PathPrefix != "a1b2c3d4e5f6" || got.FetchLimitPerHour != 5 {
		t.Fatalf("got %+v", got)
	}
}

// 库里的值被手工改坏：回内部错误，不自动重生成前缀。
func TestSubscriptionDeliveryCorrupt(t *testing.T) {
	for name, kv := range map[string]map[string]json.RawMessage{
		"前缀格式不对": {keySubscriptionPathPrefix: json.RawMessage(`"SUB"`)},
		"前缀不是串":  {keySubscriptionPathPrefix: json.RawMessage(`12`)},
		"上限为 0":  {keySubscriptionPathPrefix: json.RawMessage(`"a1b2c3d4e5f6"`), keySubscriptionFetchLimit: json.RawMessage(`0`)},
		"上限过大":   {keySubscriptionPathPrefix: json.RawMessage(`"a1b2c3d4e5f6"`), keySubscriptionFetchLimit: json.RawMessage(`10001`)},
	} {
		t.Run(name, func(t *testing.T) {
			st := &kvStore{kv: kv}
			s := newService(st, fakeTx{}, nil, nil, &auditLog{})
			_, err := s.SubscriptionDelivery(context.Background())
			var e *errs.Error
			if !errors.As(err, &e) || e.Code != errs.Internal {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// 并发的首次读取（进程内）只生成一个前缀。
func TestSubscriptionDeliveryConcurrentFirstRead(t *testing.T) {
	st := &lockedKV{kv: map[string]json.RawMessage{}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	const n = 16
	got := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.SubscriptionDelivery(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = d.PathPrefix
		}()
	}
	wg.Wait()
	for _, p := range got {
		if p != got[0] {
			t.Fatalf("前缀不一致: %v", got)
		}
	}
	var stored string
	if err := json.Unmarshal(st.kv[keySubscriptionPathPrefix], &stored); err != nil || stored != got[0] {
		t.Fatalf("落库 %q，读到 %q", stored, got[0])
	}
}

// lockedKV 是并发安全的 kvStore。
type lockedKV struct {
	fakeStore
	mu sync.Mutex
	kv map[string]json.RawMessage
}

func (k *lockedKV) Values(_ context.Context, keys []string) (map[string]json.RawMessage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		if raw, ok := k.kv[key]; ok {
			out[key] = raw
		}
	}
	return out, nil
}

func (k *lockedKV) LockValues(ctx context.Context, keys []string) (map[string]json.RawMessage, error) {
	return k.Values(ctx, keys)
}

func (k *lockedKV) InsertIfAbsent(_ context.Context, key string, value json.RawMessage) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if _, ok := k.kv[key]; ok {
		return false, nil
	}
	k.kv[key] = value
	return true, nil
}

func (k *lockedKV) Upsert(_ context.Context, key string, value json.RawMessage, _ *uuid.UUID) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kv[key] = value
	return nil
}

// 真库：前缀落进 settings 表（键名过得了库里的格式 CHECK），第二次读到同一个。
func TestSubscriptionDeliveryPG(t *testing.T) {
	svc, _, ctx, _ := newPG(t)
	first, err := svc.SubscriptionDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.SubscriptionDelivery(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.PathPrefix != second.PathPrefix || first.FetchLimitPerHour != 60 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}

// 别的进程先写好了前缀：补前缀时不覆盖，读回库里那一个。
func TestEnsurePathPrefixKeepsExisting(t *testing.T) {
	st := &lockedKV{kv: map[string]json.RawMessage{keySubscriptionPathPrefix: json.RawMessage(`"0123456789ab"`)}}
	svc := &Service{store: st}
	got, err := svc.ensurePathPrefix(context.Background())
	if err != nil || got != "0123456789ab" {
		t.Fatalf("得到 %q err=%v，应读回已有的前缀", got, err)
	}
}
