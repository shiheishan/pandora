package service

import (
	"reflect"
	"testing"

	"github.com/google/uuid"

	catalogsvc "github.com/aegispanel/aegis/internal/module/catalog/service"
)

func TestLivePlanNames(t *testing.T) {
	poolID := uuid.New()
	vDraft := uuid.New()
	vOnSale := uuid.New()
	vInUse := uuid.New()
	vAnotherInUse := uuid.New()

	tests := []struct {
		name     string
		usage    map[uuid.UUID]catalogsvc.PoolUsage
		inUse    map[uuid.UUID]bool
		wantLive map[uuid.UUID][]string
	}{
		{
			name: "只绑草稿：不在用",
			usage: map[uuid.UUID]catalogsvc.PoolUsage{
				poolID: {
					Bindings: []catalogsvc.PoolBinding{
						{VersionID: vDraft, PlanName: "草稿套餐", OnSale: false},
					},
				},
			},
			inUse: map[uuid.UUID]bool{
				vDraft: false,
			},
			wantLive: map[uuid.UUID][]string{
				poolID: {},
			},
		},
		{
			name: "在售：在用",
			usage: map[uuid.UUID]catalogsvc.PoolUsage{
				poolID: {
					Bindings: []catalogsvc.PoolBinding{
						{VersionID: vOnSale, PlanName: "在售套餐", OnSale: true},
					},
				},
			},
			inUse: map[uuid.UUID]bool{},
			wantLive: map[uuid.UUID][]string{
				poolID: {"在售套餐"},
			},
		},
		{
			name: "不在售但有人用：在用",
			usage: map[uuid.UUID]catalogsvc.PoolUsage{
				poolID: {
					Bindings: []catalogsvc.PoolBinding{
						{VersionID: vInUse, PlanName: "旧版有人用", OnSale: false},
					},
				},
			},
			inUse: map[uuid.UUID]bool{
				vInUse: true,
			},
			wantLive: map[uuid.UUID][]string{
				poolID: {"旧版有人用"},
			},
		},
		{
			name: "同名套餐多个版本去重并升序",
			usage: map[uuid.UUID]catalogsvc.PoolUsage{
				poolID: {
					Bindings: []catalogsvc.PoolBinding{
						{VersionID: vOnSale, PlanName: "套餐乙", OnSale: true},
						{VersionID: vInUse, PlanName: "套餐乙", OnSale: false},
						{VersionID: vAnotherInUse, PlanName: "套餐甲", OnSale: false},
					},
				},
			},
			inUse: map[uuid.UUID]bool{
				vInUse:        true,
				vAnotherInUse: true,
			},
			wantLive: map[uuid.UUID][]string{
				poolID: {"套餐乙", "套餐甲"}, // 按字节序升序（与 catalog 的 sort.Strings 一致），乙 U+4E59 在甲 U+7532 之前
			},
		},
		{
			name: "池没有任何绑定",
			usage: map[uuid.UUID]catalogsvc.PoolUsage{
				poolID: {
					Bindings: nil,
				},
			},
			inUse: map[uuid.UUID]bool{},
			wantLive: map[uuid.UUID][]string{
				poolID: {},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := livePlanNames(tc.usage, tc.inUse)
			if !reflect.DeepEqual(got, tc.wantLive) {
				t.Fatalf("livePlanNames() = %v, want %v", got, tc.wantLive)
			}
		})
	}
}
