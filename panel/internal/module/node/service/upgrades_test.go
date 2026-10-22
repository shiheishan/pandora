package service

// 在线升级纯函数单测（不连库）。

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/model"
)

func upgradeTestTarget() PdndTarget {
	sha := strings.Repeat("a", 64)
	return PdndTarget{
		Available: true,
		Version:   "v2026.10.21",
		Serial:    100,
		Artifacts: map[string]PdndTargetArtifact{
			"linux-amd64": {
				File:      PdndArtifactAMD64,
				SHA256:    sha,
				Available: true,
			},
		},
	}
}

func upgradeTestFacts() upgradeFacts {
	b := strings.Repeat("b", 64)
	return upgradeFacts{
		Status:       ServerReady,
		BinarySHA256: &b,
		Online:       true,
		Session: SessionUpgradeInfo{
			UpgradeV1:   true,
			BuildSerial: 1,
			Arch:        "amd64",
		},
	}
}

func TestUpgradeBlockOrder(t *testing.T) {
	target := upgradeTestTarget()
	facts := upgradeTestFacts()
	art, reason := upgradeBlock(target, facts)
	if reason != "" || art.File != PdndArtifactAMD64 {
		t.Fatalf("基准: art=%+v reason=%q", art, reason)
	}

	shaA := strings.Repeat("a", 64)

	cases := []struct {
		name   string
		target *PdndTarget
		facts  upgradeFacts
		want   string
	}{
		{
			name:  "OpenJob",
			facts: func() upgradeFacts { f := upgradeTestFacts(); f.OpenJob = true; return f }(),
			want:  UpgradeBlockInProgress,
		},
		{
			name: "manifest_missing",
			target: &PdndTarget{
				Available: false,
				Reason:    UpgradeBlockManifestMissing,
				Serial:    100,
				Artifacts: target.Artifacts,
			},
			facts: upgradeTestFacts(),
			want:  UpgradeBlockManifestMissing,
		},
		{
			name:  "not_ready",
			facts: func() upgradeFacts { f := upgradeTestFacts(); f.Status = ServerDraft; return f }(),
			want:  UpgradeBlockNotReady,
		},
		{
			name:  "not_connected",
			facts: func() upgradeFacts { f := upgradeTestFacts(); f.Online = false; return f }(),
			want:  UpgradeBlockNotConnected,
		},
		{
			name: "no_upgrade_support",
			facts: func() upgradeFacts {
				f := upgradeTestFacts()
				f.Session.UpgradeV1 = false
				return f
			}(),
			want: UpgradeBlockNoSupport,
		},
		{
			name: "arch_unsupported",
			facts: func() upgradeFacts {
				f := upgradeTestFacts()
				f.Session.Arch = "riscv64"
				return f
			}(),
			want: UpgradeBlockArchUnsupported,
		},
		{
			name: "binary_mismatch",
			target: func() *PdndTarget {
				t := upgradeTestTarget()
				a := t.Artifacts["linux-amd64"]
				a.Available = false
				a.Reason = ""
				t.Artifacts["linux-amd64"] = a
				return &t
			}(),
			facts: upgradeTestFacts(),
			want:  UpgradeBlockBinaryMismatch,
		},
		{
			name: "up_to_date",
			facts: func() upgradeFacts {
				f := upgradeTestFacts()
				f.BinarySHA256 = &shaA
				return f
			}(),
			want: UpgradeBlockUpToDate,
		},
		{
			name: "serial_not_newer",
			facts: func() upgradeFacts {
				f := upgradeTestFacts()
				f.Session.BuildSerial = 100
				return f
			}(),
			want: UpgradeBlockSerialNotNewer,
		},
		{
			name: "arch_AMD64_仍可升",
			facts: func() upgradeFacts {
				f := upgradeTestFacts()
				f.Session.Arch = "AMD64"
				return f
			}(),
			want: "",
		},
	}

	for _, tc := range cases {
		tgt := target
		if tc.target != nil {
			tgt = *tc.target
		}
		art, got := upgradeBlock(tgt, tc.facts)
		if got != tc.want {
			t.Fatalf("%s: got %q want %q", tc.name, got, tc.want)
		}
		if tc.want == "" && art.File != PdndArtifactAMD64 {
			t.Fatalf("%s: 应返回产物 %+v", tc.name, art)
		}
	}
}

func TestUpgradeJobViewUpdatedAt(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sent := base.Add(time.Hour)
	staged := base.Add(2 * time.Hour)
	finished := base.Add(3 * time.Hour)
	reason := "confirm_timeout"

	m := model.ServerUpgrade{
		ID:            uuid.MustParse("018e0000-0000-7000-8000-000000000001"),
		Status:        UpgradeRolledBack,
		TargetVersion: "v2026.10.21",
		ReasonCode:    &reason,
		CreatedAt:     base,
		SentAt:        &sent,
		StagedAt:      &staged,
		FinishedAt:    &finished,
	}
	j := upgradeJobView(m)
	if j.ID != m.ID || j.Status != m.Status || j.TargetVersion != m.TargetVersion || j.ReasonCode != m.ReasonCode {
		t.Fatalf("字段不对: %+v", j)
	}
	if !j.UpdatedAt.Equal(finished) {
		t.Fatalf("UpdatedAt=%v want %v", j.UpdatedAt, finished)
	}

	m2 := model.ServerUpgrade{CreatedAt: base, SentAt: &sent}
	if !upgradeJobView(m2).UpdatedAt.Equal(sent) {
		t.Fatal("应取 sent")
	}
	m3 := model.ServerUpgrade{CreatedAt: base}
	if !upgradeJobView(m3).UpdatedAt.Equal(base) {
		t.Fatal("只有 created")
	}
}

func TestPdndTargetReasonText(t *testing.T) {
	if pdndTargetReasonText(UpgradeBlockManifestMissing) != "面板上没有节点端发布清单" {
		t.Fatal("manifest_missing")
	}
	if pdndTargetReasonText(UpgradeBlockSignatureMissing) != "面板上没有有效的发布清单签名" {
		t.Fatal("signature_missing")
	}
	if pdndTargetReasonText(UpgradeBlockManifestInvalid) != "发布清单格式不对" {
		t.Fatal("manifest_invalid")
	}
	if pdndTargetReasonText(UpgradeBlockBinaryMismatch) != "面板托管的节点端程序与发布清单不符" {
		t.Fatal("binary_mismatch")
	}
	if pdndTargetReasonText("custom_code") != "custom_code" {
		t.Fatal("未知码应原样")
	}
}

func TestDedupeUUIDs(t *testing.T) {
	if len(dedupeUUIDs(nil)) != 0 {
		t.Fatal("nil")
	}
	if len(dedupeUUIDs([]uuid.UUID{})) != 0 {
		t.Fatal("空切片")
	}
	a := uuid.MustParse("018e0000-0000-7000-8000-000000000001")
	b := uuid.MustParse("018e0000-0000-7000-8000-000000000002")
	out := dedupeUUIDs([]uuid.UUID{a, b, a, b, a})
	if len(out) != 2 || out[0] != a || out[1] != b {
		t.Fatalf("顺序与去重: %v", out)
	}
}
