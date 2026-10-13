package handler

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/module/report/service"
)

func TestShapeSystemStatusBackupUnreadable(t *testing.T) {
	f := &fakeSvc{system: service.SystemStatus{
		Backup:   service.BackupStatus{Dir: "/backups", Readable: false, Message: "读不到"},
		Database: &service.DatabaseStats{SizeBytes: 1, Connections: 2, MaxConnections: 100},
		State:    "ok", Components: []service.Component{{Key: "postgres", State: service.StateOK, Metrics: map[string]any{}}},
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/system/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	bk := root["backup"].(map[string]any)
	wantKeys(t, "backup unreadable", bk, "dir", "readable", "message")
	if bk["readable"] != false {
		t.Fatal("readable")
	}
}

func TestShapeSystemStatusBackupReadableWithLatest(t *testing.T) {
	mod := time.Date(2026, 3, 9, 10, 0, 0, 0, time.UTC)
	age := 24
	missC, missS := 0, 0
	f := &fakeSvc{system: service.SystemStatus{
		Backup: service.BackupStatus{
			Dir: "/backups", Readable: true, Count: 2, TotalBytes: 1000, Stale: false,
			Latest:         &service.BackupFile{Name: "a.dump.age", Size: 500, ModTime: mod, HasChecksum: true, HasSeal: true},
			LatestAgeHours: &age, MissingChecksum: &missC, MissingSeal: &missS,
			Recent:            []service.BackupFile{{Name: "a.dump.age", Size: 500, ModTime: mod, HasChecksum: true, HasSeal: true}},
			OffsiteConfigured: true,
		},
		Database: &service.DatabaseStats{SizeBytes: 1, Connections: 2, MaxConnections: 100},
		State:    "ok", Components: sampleComponents(),
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/system/status", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	bk := root["backup"].(map[string]any)
	wantKeys(t, "backup readable", bk,
		"dir", "readable", "count", "total_bytes", "stale", "recent", "offsite_configured",
		"latest", "latest_age_hours", "missing_checksum", "missing_seal",
	)
	latest := bk["latest"].(map[string]any)
	wantKeys(t, "latest", latest, "name", "size", "created_at", "has_checksum", "has_seal")
	db := root["database"].(map[string]any)
	wantKeys(t, "database ok", db, "size_bytes", "connections", "max_connections")
}

func TestShapeSystemStatusBackupReadableNoLatest(t *testing.T) {
	missC, missS := 1, 1
	f := &fakeSvc{system: service.SystemStatus{
		Backup: service.BackupStatus{
			Dir: "/backups", Readable: true, Count: 1, TotalBytes: 100, Stale: true,
			MissingChecksum: &missC, MissingSeal: &missS, OffsiteConfigured: false,
		},
		Database: &service.DatabaseStats{SizeBytes: 1, Connections: 1, MaxConnections: 10},
		State:    "degraded", Components: sampleComponents(),
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/system/status", "")
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	bk := root["backup"].(map[string]any)
	if _, ok := bk["latest"]; ok {
		t.Fatal("不应有 latest")
	}
	if _, ok := bk["latest_age_hours"]; ok {
		t.Fatal("不应有 latest_age_hours")
	}
}

func TestShapeSystemStatusDatabaseError(t *testing.T) {
	f := &fakeSvc{system: service.SystemStatus{
		Backup:   service.BackupStatus{Dir: "/b", Readable: false, Message: "x"},
		Database: nil, DatabaseError: "读取数据库状态失败",
		State: "degraded", Components: sampleComponents(),
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/system/status", "")
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	db := root["database"].(map[string]any)
	wantKeys(t, "database err", db, "error")
	if _, ok := db["size_bytes"]; ok {
		t.Fatal("error 形状不应有 size_bytes")
	}
}

func TestShapeSystemStatusComponents(t *testing.T) {
	lat := 1.5
	f := &fakeSvc{system: service.SystemStatus{
		Backup:   service.BackupStatus{Dir: "/b", Readable: false, Message: "m"},
		Database: &service.DatabaseStats{SizeBytes: 1, Connections: 1, MaxConnections: 1},
		State:    "degraded",
		Components: []service.Component{
			{Key: "valkey", State: service.StateUnknown, Metrics: nil},
			{Key: "postgres", State: service.StateOK, LatencyMS: &lat, Metrics: map[string]any{"size_bytes": 1}},
		},
	}}
	w := do(newAdminEngine(f, nil, nil), http.MethodGet, "/v1/system/status", "")
	var root map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &root)
	comps := root["components"].([]any)
	unk := comps[0].(map[string]any)
	wantKeys(t, "unknown comp", unk, "key", "state", "metrics")
	if len(unk["metrics"].(map[string]any)) != 0 {
		t.Fatalf("unknown metrics 应为 {}")
	}
	if _, ok := unk["latency_ms"]; ok {
		t.Fatal("unknown 不应有 latency_ms")
	}
	okc := comps[1].(map[string]any)
	if okc["latency_ms"] == nil {
		t.Fatal("应有 latency_ms")
	}
}

func sampleComponents() []service.Component {
	return []service.Component{
		{Key: "postgres", State: service.StateOK, Metrics: map[string]any{"size_bytes": 1, "connections": 1, "max_connections": 10}},
	}
}
