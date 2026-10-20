package service

// 会话层在线升级（session_upgrade.go）的单测：能力协商、按服务器查升级事实、下发 Command.upgrade、收回报。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestUpgradeFeatureNegotiated(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{
		Features:    []string{FeatureUsersDelta, FeatureUpgradeV1},
		BuildSerial: 42,
		Host:        HostInfo{Arch: " AMD64 "},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	out := nextNow(t, s, time.Second)
	if out.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome，实际 %v", out.Kind)
	}
	if len(out.Welcome.Features) != 2 {
		t.Fatalf("Features 应含两项，得 %v", out.Welcome.Features)
	}
	for _, want := range []string{FeatureUsersDelta, FeatureUpgradeV1} {
		found := false
		for _, f := range out.Welcome.Features {
			if f == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("Welcome.Features 应含 %q，得 %v", want, out.Welcome.Features)
		}
	}

	info, ok := m.UpgradeInfo(serverID)
	if !ok {
		t.Fatal("UpgradeInfo 应为 ok")
	}
	want := SessionUpgradeInfo{UpgradeV1: true, BuildSerial: 42, Arch: "amd64"}
	if info != want {
		t.Fatalf("UpgradeInfo 得 %+v，要 %+v", info, want)
	}
}

func TestUpgradeInfoOffline(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()

	_, ok := m.UpgradeInfo(serverID)
	if ok {
		t.Fatal("未开会话时 UpgradeInfo 应为 ok=false")
	}
	if m.SendUpgrade(serverID, UpgradeCommand{JobID: uuid.New()}) {
		t.Fatal("未开会话时 SendUpgrade 应为 false")
	}
}

func TestSendUpgradeRequiresFeature(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{
		BuildSerial: 7,
		Features:    []string{FeatureUsersDelta},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	info, ok := m.UpgradeInfo(serverID)
	if !ok {
		t.Fatal("UpgradeInfo 应为 ok")
	}
	if info.UpgradeV1 {
		t.Fatal("未协商 upgrade_v1 时 UpgradeV1 应为 false")
	}
	if info.BuildSerial != 7 {
		t.Fatalf("BuildSerial 应为 7，得 %d", info.BuildSerial)
	}

	_ = nextNow(t, s, time.Second) // Welcome

	if m.SendUpgrade(serverID, UpgradeCommand{JobID: uuid.New()}) {
		t.Fatal("未协商 upgrade_v1 时 SendUpgrade 应为 false")
	}
	for _, o := range pending(s) {
		if o.Kind == OutUpgrade {
			t.Fatal("队列里不应有 OutUpgrade")
		}
	}
}

func TestSendUpgradeEnqueues(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{
		Features: []string{FeatureUpgradeV1},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second) // Welcome

	jobID := uuid.New()
	cmd := UpgradeCommand{
		JobID:            jobID,
		Manifest:         []byte("m"),
		Signature:        []byte("s"),
		BinaryURL:        "https://panel.example.com/pdnd/bin/pandora-native-linux-amd64",
		ConfirmWithinSec: 180,
	}
	if !m.SendUpgrade(serverID, cmd) {
		t.Fatal("SendUpgrade 应为 true")
	}

	var found *Outbound
	for _, o := range pending(s) {
		if o.Kind == OutUpgrade {
			found = &o
			break
		}
	}
	if found == nil {
		t.Fatal("pending 里应有 OutUpgrade")
	}
	if found.Upgrade.JobID != cmd.JobID {
		t.Fatalf("JobID 不一致")
	}
	if !bytes.Equal(found.Upgrade.Manifest, cmd.Manifest) {
		t.Fatalf("Manifest 不一致")
	}
	if !bytes.Equal(found.Upgrade.Signature, cmd.Signature) {
		t.Fatalf("Signature 不一致")
	}
	if found.Upgrade.BinaryURL != cmd.BinaryURL {
		t.Fatalf("BinaryURL 不一致")
	}
	if found.Upgrade.ConfirmWithinSec != cmd.ConfirmWithinSec {
		t.Fatalf("ConfirmWithinSec 不一致")
	}
}

func TestSendUpgradeAfterTombstone(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{
		Features: []string{FeatureUpgradeV1},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	m.Tombstone(serverID, "x")

	_, ok := m.UpgradeInfo(serverID)
	if ok {
		t.Fatal("Tombstone 后 UpgradeInfo 应为 ok=false")
	}
	if m.SendUpgrade(serverID, UpgradeCommand{JobID: uuid.New()}) {
		t.Fatal("Tombstone 后 SendUpgrade 应为 false")
	}

	_ = s // 会话仍在，供 Tombstone 队列行为与 session_test 一致
}

func TestHandleUpgradeReport(t *testing.T) {
	b := newFakeBackend()
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	ctx := context.Background()

	s, err := m.Open(ctx, serverID, Hello{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	_ = nextNow(t, s, time.Second)

	id := uuid.New()
	r := UpgradeReport{JobID: id, Phase: UpgradePhaseStaged}
	s.HandleUpgradeReport(ctx, r)

	b.mu.Lock()
	reports := append([]UpgradeReport(nil), b.upgradeReports...)
	b.mu.Unlock()
	if len(reports) != 1 {
		t.Fatalf("upgradeReports 应有 1 条，得 %d", len(reports))
	}
	if reports[0].JobID != r.JobID || reports[0].Phase != r.Phase {
		t.Fatalf("记录与发送不一致：%+v", reports[0])
	}

	b.mu.Lock()
	b.upgradeErr = errors.New("boom")
	b.mu.Unlock()
	s.HandleUpgradeReport(ctx, r)
	if s.Err() != nil {
		t.Fatalf("记录失败不应结束会话，Err=%v", s.Err())
	}
}

// Hello 带来的升级结局没记上：建流失败、不发 Welcome，节点留着结局文件重连再报；别的 Hello 写失败照常建流
func TestOpenFailsWhenUpgradeOutcomeUnrecorded(t *testing.T) {
	b := newFakeBackend()
	b.helloErr = fmt.Errorf("%w: 库暂时不可用", ErrUpgradeOutcomeUnrecorded)
	m := NewSessionManager(b, nil, fastOptions())
	serverID := uuid.New()
	if _, err := m.Open(context.Background(), serverID, Hello{Features: []string{FeatureUpgradeV1}}); !errors.Is(err, ErrUpgradeOutcomeUnrecorded) {
		t.Fatalf("应建流失败，得 %v", err)
	}
	if _, ok := m.UpgradeInfo(serverID); ok {
		t.Fatal("建流失败后不应留着会话")
	}

	b.helloErr = errors.New("主机信息写失败")
	s, err := m.Open(context.Background(), serverID, Hello{Features: []string{FeatureUpgradeV1}})
	if err != nil {
		t.Fatalf("别的 Hello 写失败应照常建流: %v", err)
	}
	if out := nextNow(t, s, time.Second); out.Kind != OutWelcome {
		t.Fatalf("第一条应为 Welcome，实际 %v", out.Kind)
	}
}
