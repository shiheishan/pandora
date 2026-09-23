package admin

import (
	"os"
	"strings"
	"testing"
)

func TestNodeStatusLockSQLHasValidProtocolReadyCoalesce(t *testing.T) {
	query := nodeStatusLockSQL()
	depth := 0
	for _, r := range query {
		switch r {
		case '(':
			depth++
		case ')':
			depth--
			if depth < 0 {
				t.Fatalf("SQL has an unmatched closing parenthesis: %s", query)
			}
		}
	}
	if depth != 0 {
		t.Fatalf("SQL has %d unmatched opening parentheses: %s", depth, query)
	}
	if !strings.Contains(query, "config_validated_at IS NOT NULL), false)") {
		t.Fatalf("protocol-ready expression is not the first COALESCE argument: %s", query)
	}
	if strings.Contains(query, "config_validated_at IS NOT NULL)), false)") {
		t.Fatalf("protocol-ready expression closes COALESCE before its fallback: %s", query)
	}
}

func TestLegacyTerminalNodeStatusRevokesDeliveryAndIdentity(t *testing.T) {
	body, err := os.ReadFile("handlers.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(body)
	start := strings.Index(src, `func (h *handlers) nodeSetStatus`)
	end := strings.Index(src, `func projectNodeLifecycle`)
	if start < 0 || end <= start {
		t.Fatal("nodeSetStatus source block not found")
	}
	block := src[start:end]
	for _, needle := range []string{
		`terminal := req.Status == "retired" || req.Status == "destroyed"`,
		`pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
		`"node-config-release/"+tenantID`,
		`desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END`,
		`UPDATE node_identities`,
		`WHERE tenant_id=$1 AND node_id=$2 AND status='active'`,
	} {
		if !strings.Contains(block, needle) {
			t.Fatalf("legacy terminal status contract missing %q", needle)
		}
	}
	lockAt := strings.Index(block, `pg_catalog.pg_advisory_xact_lock`)
	nodeAt := strings.Index(block, `nodeStatusLockSQL()`)
	if lockAt < 0 || nodeAt <= lockAt {
		t.Fatalf("legacy terminal release/node lock order drifted: release=%d node=%d", lockAt, nodeAt)
	}
}

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
			serving, server := projectNodeLifecycle(tt.node, tt.ready)
			if serving != tt.serving || server != tt.server {
				t.Fatalf("projectNodeLifecycle(%q)=(%q,%q), want (%q,%q)",
					tt.node, serving, server, tt.serving, tt.server)
			}
		})
	}
}
