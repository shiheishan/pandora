package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func newDevicesService(st *kvStore, al *auditLog) *Service {
	return newService(st, fakeTx{}, nil, nil, al)
}

func TestDevicePolicyDefaults(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newDevicesService(st, &auditLog{})
	p, err := s.DevicePolicy(context.Background())
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.Mode != DeviceModeLoose || p.Grace != 1 || p.WindowMinutes != 5 {
		t.Fatalf("defaults: mode=%q grace=%d window=%d", p.Mode, p.Grace, p.WindowMinutes)
	}
	if p.Window() != 5*time.Minute {
		t.Fatalf("window: %v", p.Window())
	}
	if p.Strict() {
		t.Fatal("loose mode should not be strict")
	}
}

func TestDevicePolicyInvalidStoredFallsBack(t *testing.T) {
	ctx := context.Background()
	st := &kvStore{kv: map[string]json.RawMessage{
		keyDevicesMode:   json.RawMessage(`"weird"`),
		keyDevicesGrace:  json.RawMessage(`9`),
		keyDevicesWindow: json.RawMessage(`7`),
	}}
	s := newDevicesService(st, &auditLog{})
	p, err := s.DevicePolicy(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.Mode != DeviceModeLoose || p.Grace != 1 || p.WindowMinutes != 5 {
		t.Fatalf("invalid stored: mode=%q grace=%d window=%d", p.Mode, p.Grace, p.WindowMinutes)
	}

	st2 := &kvStore{kv: map[string]json.RawMessage{
		keyDevicesGrace: json.RawMessage(`"abc"`),
	}}
	p2, err := newDevicesService(st2, &auditLog{}).DevicePolicy(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p2.Grace != 1 {
		t.Fatalf("grace type mismatch: %d", p2.Grace)
	}

	st3 := &kvStore{kv: map[string]json.RawMessage{
		keyDevicesMode:   json.RawMessage(`"strict"`),
		keyDevicesGrace:  json.RawMessage(`3`),
		keyDevicesWindow: json.RawMessage(`30`),
	}}
	p3, err := newDevicesService(st3, &auditLog{}).DevicePolicy(ctx)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if p3.Mode != DeviceModeStrict || p3.Grace != 3 || p3.WindowMinutes != 30 {
		t.Fatalf("valid stored: mode=%q grace=%d window=%d", p3.Mode, p3.Grace, p3.WindowMinutes)
	}
}

func TestSetDevicePolicyValidation(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newDevicesService(st, al)
	ctx := context.Background()

	grace6 := 6
	err := s.SetDevicePolicy(ctx, DevicePolicyUpdate{Mode: DeviceModeLoose, Grace: &grace6})
	assertFieldValidation(t, err, "grace")

	graceNeg := -1
	err = s.SetDevicePolicy(ctx, DevicePolicyUpdate{Mode: DeviceModeLoose, Grace: &graceNeg})
	assertFieldValidation(t, err, "grace")

	win7 := 7
	err = s.SetDevicePolicy(ctx, DevicePolicyUpdate{Mode: DeviceModeLoose, WindowMinutes: &win7})
	assertFieldValidation(t, err, "window_minutes")

	err = s.SetDevicePolicy(ctx, DevicePolicyUpdate{Mode: "x"})
	assertFieldValidation(t, err, "mode")

	if len(st.kv) != 0 {
		t.Fatalf("store should be empty, got %d keys", len(st.kv))
	}
	if len(al.entries) != 0 {
		t.Fatalf("audit should be empty, got %d entries", len(al.entries))
	}
}

func TestSetDevicePolicyPersistsAndAudits(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newDevicesService(st, al)
	ctx := context.Background()

	grace2 := 2
	win30 := 30
	if err := s.SetDevicePolicy(ctx, DevicePolicyUpdate{
		Mode:          DeviceModeStrict,
		Grace:         &grace2,
		WindowMinutes: &win30,
	}); err != nil {
		t.Fatalf("set: %v", err)
	}

	p, err := s.DevicePolicy(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Mode != DeviceModeStrict || p.Grace != 2 || p.WindowMinutes != 30 {
		t.Fatalf("read back: mode=%q grace=%d window=%d", p.Mode, p.Grace, p.WindowMinutes)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	e := al.entries[0]
	if e.action != auditDevicesChanged || e.resourceType != auditResourceSettings {
		t.Fatalf("audit: %s %s", e.action, e.resourceType)
	}

	if err := s.SetDevicePolicy(ctx, DevicePolicyUpdate{
		Mode:          DeviceModeStrict,
		Grace:         &grace2,
		WindowMinutes: &win30,
	}); err != nil {
		t.Fatalf("noop set: %v", err)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit after noop set: %d", len(al.entries))
	}

	if err := s.SetDevicePolicy(ctx, DevicePolicyUpdate{Mode: DeviceModeLoose}); err != nil {
		t.Fatalf("mode only: %v", err)
	}
	p, err = s.DevicePolicy(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if p.Mode != DeviceModeLoose || p.Grace != 2 || p.WindowMinutes != 30 {
		t.Fatalf("partial update: mode=%q grace=%d window=%d", p.Mode, p.Grace, p.WindowMinutes)
	}
}

func TestValidDeviceWindow(t *testing.T) {
	for _, m := range []int{5, 10, 30, 60} {
		if !ValidDeviceWindow(m) {
			t.Fatalf("%d should be valid", m)
		}
	}
	for _, m := range []int{0, 7, 15, 120} {
		if ValidDeviceWindow(m) {
			t.Fatalf("%d should be invalid", m)
		}
	}
}
