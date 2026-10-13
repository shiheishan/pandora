package service

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
	settingssvc "github.com/aegispanel/aegis/internal/module/settings/service"
	subsvc "github.com/aegispanel/aegis/internal/module/subscription/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/testkit"
)

// devFakeTx：无事务上下文时 AfterCommit 立即执行。
type devFakeTx struct{}

func (devFakeTx) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type devFakeSettings struct {
	policy   settingssvc.DevicePolicy
	readErr  error
	writeErr error
	updates  []settingssvc.DevicePolicyUpdate
}

func (f *devFakeSettings) DevicePolicy(context.Context) (settingssvc.DevicePolicy, error) {
	if f.readErr != nil {
		return settingssvc.DevicePolicy{}, f.readErr
	}
	return f.policy, nil
}

func (f *devFakeSettings) SetDevicePolicy(_ context.Context, u settingssvc.DevicePolicyUpdate) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.updates = append(f.updates, u)
	return nil
}

type devFakeSubs struct{}

func (devFakeSubs) SubscriptionsByIDs(context.Context, []uuid.UUID) (map[uuid.UUID]subsvc.Subscription, error) {
	return map[uuid.UUID]subsvc.Subscription{}, nil
}

type devFakeVersions struct{}

func (devFakeVersions) VersionsByIDs(context.Context, []uuid.UUID) (map[uuid.UUID]catalogsvc.Version, error) {
	return map[uuid.UUID]catalogsvc.Version{}, nil
}

func TestDeviceExceeded(t *testing.T) {
	cases := []struct {
		limit, online, grace int
		want                 bool
	}{
		{0, 99, 0, false},
		{2, 3, 1, false},
		{2, 4, 1, true},
		{1, 2, 0, true},
		{1, 1, 0, false},
	}
	for _, c := range cases {
		if got := deviceExceeded(c.limit, c.online, c.grace); got != c.want {
			t.Fatalf("deviceExceeded(%d,%d,%d)=%v 期望 %v", c.limit, c.online, c.grace, got, c.want)
		}
	}
}

func TestStrictDigest(t *testing.T) {
	a := uuid.MustParse("00000000-0000-4000-8000-000000000001")
	b := uuid.MustParse("00000000-0000-4000-8000-000000000002")
	c := uuid.MustParse("00000000-0000-4000-8000-000000000003")

	d1 := strictDigest([]uuid.UUID{a, b})
	d2 := strictDigest([]uuid.UUID{b, a})
	if d1 != d2 {
		t.Fatalf("顺序不同摘要应相同: %q vs %q", d1, d2)
	}
	if strictDigest([]uuid.UUID{a, b}) == strictDigest([]uuid.UUID{a, b, c}) {
		t.Fatal("多一个 id 摘要应不同")
	}
	dNil := strictDigest(nil)
	dEmpty := strictDigest([]uuid.UUID{})
	if dNil != dEmpty {
		t.Fatalf("nil 与空切片摘要应相同: %q vs %q", dNil, dEmpty)
	}
	if len(dNil) != 64 {
		t.Fatalf("摘要应为 64 位 hex，得 %d 字符", len(dNil))
	}
}

func TestSetDevicePolicyBumpsUsersEpoch(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	svc := New(nil, nil, nil, rdb)
	svc.tx = devFakeTx{}
	fake := &devFakeSettings{policy: settingssvc.DefaultDevicePolicy()}
	svc.UseDevices(fake, devFakeSubs{}, nil, nil)
	two := 2
	if err := svc.SetDevicePolicy(context.Background(), DevicePolicyInput{Mode: settingssvc.DeviceModeStrict, Grace: &two}); err != nil {
		t.Fatalf("SetDevicePolicy: %v", err)
	}
	if len(fake.updates) != 1 {
		t.Fatalf("期望 1 个 update，得 %d", len(fake.updates))
	}
	if fake.updates[0].Mode != settingssvc.DeviceModeStrict || fake.updates[0].Grace == nil || *fake.updates[0].Grace != 2 {
		t.Fatalf("update 内容不对: %+v", fake.updates[0])
	}
	if v, err := mr.Get(UsersEpochKey); err != nil || v != "1" {
		t.Fatalf("名单纪元 %q %v，期望 1", v, err)
	}
}

func TestSetDevicePolicyInvalidNoWrite(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	svc := New(nil, nil, nil, rdb)
	svc.tx = devFakeTx{}
	fake := &devFakeSettings{}
	svc.UseDevices(fake, nil, nil, nil)
	err := svc.SetDevicePolicy(context.Background(), DevicePolicyInput{Mode: "x"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.ValidationFailed {
		t.Fatalf("期望 ValidationFailed，得 %v", err)
	}
	if len(fake.updates) != 0 {
		t.Fatal("非法模式不应写入")
	}
	if mr.Exists(UsersEpochKey) {
		t.Fatal("纪元键不应存在")
	}
}

func TestSetDevicePolicyWriteErrorNoBump(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	svc := New(nil, nil, nil, rdb)
	svc.tx = devFakeTx{}
	want := errors.New("write failed")
	fake := &devFakeSettings{writeErr: want}
	svc.UseDevices(fake, nil, nil, nil)
	err := svc.SetDevicePolicy(context.Background(), DevicePolicyInput{Mode: settingssvc.DeviceModeLoose})
	if !errors.Is(err, want) {
		t.Fatalf("期望 writeErr，得 %v", err)
	}
	if mr.Exists(UsersEpochKey) {
		t.Fatal("纪元键不应存在")
	}
}

func TestSetDevicePolicyPortMissing(t *testing.T) {
	_, rdb := testkit.Redis(t)
	svc := New(nil, nil, nil, rdb)
	svc.tx = devFakeTx{}
	err := svc.SetDevicePolicy(context.Background(), DevicePolicyInput{Mode: settingssvc.DeviceModeLoose})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.Unavailable {
		t.Fatalf("期望 Unavailable，得 %v", err)
	}
}

func TestDevicePolicyFallsBackOnReadError(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	want := settingssvc.DefaultDevicePolicy()
	if p := svc.devicePolicy(context.Background()); p != want {
		t.Fatalf("未装配 settings 应缺省: %+v", p)
	}
	svc.UseDevices(&devFakeSettings{readErr: errors.New("read")}, nil, nil, nil)
	if p := svc.devicePolicy(context.Background()); p != want {
		t.Fatalf("读失败应缺省: %+v", p)
	}
}

func TestRecheckStrictLooseClearsDigest(t *testing.T) {
	mr, rdb := testkit.Redis(t)
	if err := mr.Set(StrictDigestKey, "x"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	svc := New(nil, nil, nil, rdb)
	svc.UseDevices(&devFakeSettings{policy: settingssvc.DefaultDevicePolicy()}, devFakeSubs{}, nil, nil)
	svc.UseDelivery(nil, nil, devFakeVersions{})
	if err := svc.RecheckStrictDevices(context.Background()); err != nil {
		t.Fatalf("RecheckStrictDevices: %v", err)
	}
	if mr.Exists(StrictDigestKey) {
		t.Fatal("loose 应删掉摘要键")
	}
	if mr.Exists(UsersEpochKey) {
		t.Fatal("纪元键不应存在")
	}
}

func TestRecheckStrictNoRedis(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	if err := svc.RecheckStrictDevices(context.Background()); err != nil {
		t.Fatalf("无 Redis 应直接 nil: %v", err)
	}
}
