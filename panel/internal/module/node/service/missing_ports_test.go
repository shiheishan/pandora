package service

import (
	"reflect"
	"testing"
)

func TestMissingPortsZeroValueListsAllRequired(t *testing.T) {
	var s Service
	want := []string{
		"Catalog",
		"UserGroups",
		"VersionsInUse",
		"IdemCompleter",
		"TrafficCharger",
		"DeviceKeyHasher",
		"ServingSubscriptions",
		"ServingUsers",
		"PlanVersions",
		"DeviceSettings",
		"DeviceSubscriptions",
		"DeviceUsers",
		"DevicePlans",
		"Realtime",
		"SiteSettings",
		"ECHSealer",
	}
	got := s.MissingPorts()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingPorts() = %v, want %v", got, want)
	}
}
