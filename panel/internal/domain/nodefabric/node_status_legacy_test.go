package nodefabric

import "testing"

// ProjectNodeLifecycle 仍被一步上线使用。旧的单节点状态接口删掉之后，这张投影表留在这里。
func TestProjectNodeLifecycle(t *testing.T) {
	tests := []struct {
		node, serving, server string
		ready                 bool
	}{
		{"standby", "draft", "draft", false},
		{"canary", "active", "ready", true},
		{"canary", "disabled", "ready", false},
		{"active", "active", "ready", true},
		{"active", "disabled", "ready", false},
		{"draining", "draining", "draining", true},
		{"draining", "disabled", "draining", false},
		{"maintenance", "disabled", "maintenance", true},
		{"unhealthy", "disabled", "unhealthy", true},
		{"quarantined", "disabled", "quarantined", true},
		{"retired", "retired", "retired", true},
		{"destroyed", "retired", "retired", true},
	}
	for _, tt := range tests {
		t.Run(tt.node, func(t *testing.T) {
			serving, server := ProjectNodeLifecycle(tt.node, tt.ready)
			if serving != tt.serving || server != tt.server {
				t.Fatalf("ProjectNodeLifecycle(%q)=(%q,%q), want (%q,%q)",
					tt.node, serving, server, tt.serving, tt.server)
			}
		})
	}
}
