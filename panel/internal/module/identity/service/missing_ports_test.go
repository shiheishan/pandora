package service

import (
	"reflect"
	"testing"
)

func TestMissingPortsZeroValueListsAllRequired(t *testing.T) {
	var s Service
	want := []string{
		"SessionRevoker",
		"AdminGuard",
		"StaffGuard",
		"VerificationMailer",
		"RosterNotifier",
		"OpsSubscriptions",
		"OpsOrders",
		"OpsWallets",
		"OpsTelegram",
		"OpsGroupUsage",
		"JobQueue",
		"PluginEvents",
		"TrialGranter",
		"AccountCloser",
		"OrderCanceller",
		"TelegramUnbinder",
	}
	got := s.MissingPorts()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MissingPorts() = %v, want %v", got, want)
	}
}
