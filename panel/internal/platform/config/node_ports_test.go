package config

import (
	"reflect"
	"testing"
)

func clearNodePortsEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"AEGIS_NODE_RESERVED_PORTS", "AEGIS_PANEL_RESERVED_PORTS", "AEGIS_PANEL_HOSTS"} {
		t.Setenv(k, "")
	}
}

func TestNodePortsDefaults(t *testing.T) {
	clearNodePortsEnv(t)
	got, err := loadNodePorts("https://Panel.Example.TEST/")
	if err != nil {
		t.Fatal(err)
	}
	want := NodePorts{
		Reserved:      []PortRange{{22, 22}, {25, 25}, {53, 53}, {80, 80}},
		PanelReserved: []PortRange{{80, 80}, {443, 443}, {5432, 5432}, {6379, 6379}, {9000, 9003}},
		PanelHosts:    []string{"panel.example.test"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestNodePortsOverridesAndRejects(t *testing.T) {
	clearNodePortsEnv(t)
	t.Setenv("AEGIS_NODE_RESERVED_PORTS", "22, 2000-2010")
	t.Setenv("AEGIS_PANEL_RESERVED_PORTS", "none")
	t.Setenv("AEGIS_PANEL_HOSTS", "203.0.113.7, Edge.Example.TEST")
	got, err := loadNodePorts("http://127.0.0.1:9000")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Reserved, []PortRange{{22, 22}, {2000, 2010}}) || len(got.PanelReserved) != 0 {
		t.Fatalf("ranges = %+v / %+v", got.Reserved, got.PanelReserved)
	}
	if !reflect.DeepEqual(got.PanelHosts, []string{"127.0.0.1", "203.0.113.7", "edge.example.test"}) {
		t.Fatalf("hosts = %v", got.PanelHosts)
	}
	for _, bad := range []string{"0", "65536", "9003-9000", "ssh", "22-", "1-2-3"} {
		t.Setenv("AEGIS_NODE_RESERVED_PORTS", bad)
		if _, err := loadNodePorts(""); err == nil {
			t.Errorf("AEGIS_NODE_RESERVED_PORTS=%q accepted", bad)
		}
	}
	t.Setenv("AEGIS_NODE_RESERVED_PORTS", "")
	t.Setenv("AEGIS_PANEL_HOSTS", "a.example.test/evil")
	if _, err := loadNodePorts(""); err == nil {
		t.Error("AEGIS_PANEL_HOSTS with a slash accepted")
	}
}
