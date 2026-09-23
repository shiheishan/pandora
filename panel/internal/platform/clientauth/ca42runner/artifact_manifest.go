package ca42runner

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	migrationInventoryCount = 42
	migrationManifestMax    = 4 << 20
)

var (
	artifactSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

var ca42MigrationNames = [...]string{
	"00001_foundation.sql", "00002_identity.sql", "00003_catalog_subscription.sql",
	"00004_billing_ledger.sql", "00005_node_fabric.sql", "00006_metering.sql",
	"00007_client_delivery.sql", "00008_ops_marketing.sql", "00009_security_audit.sql",
	"00010_seed_rbac.sql", "00011_app_role.sql", "00012_audit_node_actor.sql",
	"00013_xboard_node_protocol.sql", "00014_subscription_proxy_uuid.sql", "00015_node_metrics.sql",
	"00016_node_kernel.sql", "00017_node_routing.sql", "00018_subscription_delivery.sql",
	"00019_subscription_token_vault.sql", "00020_change_notify.sql", "00021_notify_fix_tables.sql",
	"00022_notify_exclude_messages.sql", "00023_notification_seed.sql", "00024_device_limit_modes.sql",
	"00025_audit_ip_vault.sql", "00026_ip_cluster_window.sql", "00027_drop_cluster_idx.sql",
	"00028_commission_settings.sql", "00029_commission_defaults.sql", "00030_mail_settings.sql",
	"00031_pandora_brand.sql", "00032_revenue_report_adjustments.sql", "00033_server_node_split.sql",
	"00034_node_admin_concurrency.sql", "00035_catalog_authoring.sql", "00036_order_reservations.sql",
	"00037_idempotency_runtime_hardening.sql", "00038_idempotency_resource_binding.sql",
	"00039_bound_idempotency_success.sql", "00040_order_release_and_late_suspense.sql",
	"00041_dashboard_read_models.sql", "00042_client_auth_expand.sql",
}

type migrationInventoryEntry struct {
	name   string
	digest [sha256.Size]byte
}

func parseMigrationInventory(data []byte, expectedSetSHA256, expected00042SHA256 string) ([]migrationInventoryEntry, error) {
	if len(data) == 0 || len(data) > migrationManifestMax || data[len(data)-1] != '\n' ||
		bytes.IndexByte(data, '\r') >= 0 || bytes.IndexByte(data, 0) >= 0 ||
		bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) || !utf8.Valid(data) {
		return nil, errors.New("CA42 migration inventory envelope invalid")
	}
	if !artifactSHA256Pattern.MatchString(expectedSetSHA256) || !artifactSHA256Pattern.MatchString(expected00042SHA256) {
		return nil, errors.New("CA42 migration inventory expected hash invalid")
	}
	digest := sha256.Sum256(data)
	expectedSet, _ := hex.DecodeString(expectedSetSHA256)
	if subtle.ConstantTimeCompare(digest[:], expectedSet) != 1 {
		return nil, errors.New("CA42 migration inventory identity mismatch")
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	if len(lines) != migrationInventoryCount {
		return nil, errors.New("CA42 migration inventory must contain exactly 00001 through 00042")
	}
	entries := make([]migrationInventoryEntry, 0, migrationInventoryCount)
	for index, line := range lines {
		if len(line) < sha256.Size*2+3 || line[sha256.Size*2:sha256.Size*2+2] != "  " {
			return nil, fmt.Errorf("CA42 migration inventory line %d invalid", index+1)
		}
		digestText, name := line[:sha256.Size*2], line[sha256.Size*2+2:]
		if !artifactSHA256Pattern.MatchString(digestText) || digestText == strings.Repeat("0", sha256.Size*2) || name != ca42MigrationNames[index] {
			return nil, fmt.Errorf("CA42 migration inventory line %d invalid", index+1)
		}
		decoded, _ := hex.DecodeString(digestText)
		var entryDigest [sha256.Size]byte
		copy(entryDigest[:], decoded)
		entries = append(entries, migrationInventoryEntry{name: name, digest: entryDigest})
	}
	expected00042, _ := hex.DecodeString(expected00042SHA256)
	if subtle.ConstantTimeCompare(entries[len(entries)-1].digest[:], expected00042) != 1 {
		return nil, errors.New("CA42 migration 00042 inventory hash mismatch")
	}
	return entries, nil
}
