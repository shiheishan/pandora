package service

import "testing"

// nodeAllowedEdges 是节点服务状态允许的 (from→to) 边集合。
var nodeAllowedEdges = map[string]map[string]bool{
	NodeDraft:    {NodeActive: true, NodeRetired: true},
	NodeActive:   {NodeDisabled: true, NodeRetired: true},
	NodeDisabled: {NodeActive: true, NodeRetired: true},
	NodeRetired:  {},
}

var nodeAllStatuses = []string{NodeDraft, NodeActive, NodeDisabled, NodeRetired}

func TestCanNodeTransition_fullMatrix(t *testing.T) {
	for _, from := range nodeAllStatuses {
		for _, to := range nodeAllStatuses {
			want := false
			if from == to {
				want = false
			} else if m, ok := nodeAllowedEdges[from]; ok {
				want = m[to]
			}
			got := CanNodeTransition(from, to)
			if got != want {
				t.Errorf("CanNodeTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanNodeTransition_unknownState(t *testing.T) {
	for _, to := range nodeAllStatuses {
		if CanNodeTransition("unhealthy", to) {
			t.Errorf("unknown from unhealthy → %q should be false", to)
		}
	}
	for _, from := range nodeAllStatuses {
		if CanNodeTransition(from, "quarantined") {
			t.Errorf("unknown to quarantined from %q should be false", from)
		}
	}
	if CanNodeTransition("draining", NodeActive) {
		t.Error("unknown from draining should be false")
	}
}

func TestValidNodeServingStatus(t *testing.T) {
	for _, s := range nodeAllStatuses {
		if !ValidNodeServingStatus(s) {
			t.Errorf("ValidNodeServingStatus(%q) want true", s)
		}
	}
	for _, bad := range []string{"", "unhealthy", "quarantined", "draining", "pending"} {
		if ValidNodeServingStatus(bad) {
			t.Errorf("ValidNodeServingStatus(%q) want false", bad)
		}
	}
}

// serverAllowedEdges 是服务器状态允许的 (from→to) 边集合。
var serverAllowedEdges = map[string]map[string]bool{
	ServerDraft: {
		ServerReady: true, ServerMaintenance: true, ServerRetired: true,
	},
	ServerReady: {
		ServerDraining: true, ServerMaintenance: true,
	},
	ServerDraining: {
		ServerReady: true, ServerMaintenance: true, ServerRetired: true,
	},
	ServerMaintenance: {
		ServerReady: true, ServerDraining: true, ServerRetired: true,
	},
	ServerRetired: {},
}

var serverAllStatuses = []string{
	ServerDraft, ServerReady, ServerDraining, ServerMaintenance, ServerRetired,
}

func TestCanServerTransition_fullMatrix(t *testing.T) {
	for _, from := range serverAllStatuses {
		for _, to := range serverAllStatuses {
			want := false
			if from == to {
				want = false
			} else if m, ok := serverAllowedEdges[from]; ok {
				want = m[to]
			}
			got := CanServerTransition(from, to)
			if got != want {
				t.Errorf("CanServerTransition(%q, %q) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestCanServerTransition_readyNotToRetired(t *testing.T) {
	if CanServerTransition(ServerReady, ServerRetired) {
		t.Error("ready 不能直达 retired")
	}
}

func TestCanServerTransition_retiredNoOutgoing(t *testing.T) {
	for _, to := range serverAllStatuses {
		if CanServerTransition(ServerRetired, to) {
			t.Errorf("retired 不应有出边 → %q", to)
		}
	}
}

func TestCanServerTransition_unknownState(t *testing.T) {
	if CanServerTransition("unhealthy", ServerReady) {
		t.Error("unknown from should be false")
	}
	if CanServerTransition(ServerReady, "quarantined") {
		t.Error("unknown to should be false")
	}
}

func TestValidServerStatus(t *testing.T) {
	for _, s := range serverAllStatuses {
		if !ValidServerStatus(s) {
			t.Errorf("ValidServerStatus(%q) want true", s)
		}
	}
	for _, bad := range []string{"", "unhealthy", "quarantined", "active"} {
		if ValidServerStatus(bad) {
			t.Errorf("ValidServerStatus(%q) want false", bad)
		}
	}
}
