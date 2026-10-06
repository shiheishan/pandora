// [INPUT]: 依赖 config.go 与 deployment.go 的包内解析函数
// [OUTPUT]: 对外提供 令牌时长校验、严格时长解析、部署项变量名与缺省值的单元测试
// [POS]: platform/config 的解析单测；源码守卫在 envaccess_test.go

package config

import (
	"testing"
	"time"
)

func TestValidateTokenTTLs(t *testing.T) {
	for _, tt := range []struct {
		name            string
		access, refresh time.Duration
		wantErr         bool
	}{
		{name: "default thirty days", access: 30 * 24 * time.Hour, refresh: 30 * 24 * time.Hour},
		{name: "short access long refresh", access: time.Hour, refresh: 30 * 24 * time.Hour},
		{name: "zero access", access: 0, refresh: time.Hour, wantErr: true},
		{name: "negative refresh", access: time.Hour, refresh: -time.Hour, wantErr: true},
		{name: "access exceeds refresh", access: 2 * time.Hour, refresh: time.Hour, wantErr: true},
		{name: "access too large", access: 366 * 24 * time.Hour, refresh: 366 * 24 * time.Hour, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTokenTTLs(tt.access, tt.refresh)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateTokenTTLs(%s, %s) error=%v wantErr=%v", tt.access, tt.refresh, err, tt.wantErr)
			}
		})
	}
}

func TestStrictEnvDurationRejectsInvalidValue(t *testing.T) {
	t.Setenv("AEGIS_ACCESS_TOKEN_TTL", "thirty-days")
	if _, err := strictEnvDuration("AEGIS_ACCESS_TOKEN_TTL", 30*24*time.Hour); err == nil {
		t.Fatal("invalid duration was silently replaced by the default")
	}
}

// 部署项的变量名、缺省值与解析口径是 deploy 的 EnvironmentFile 依赖的契约，改动即红。
func TestLoadDeploymentKeepsNamesAndDefaults(t *testing.T) {
	for _, k := range []string{"AEGIS_BACKUP_DIR", "AEGIS_BACKUP_AGE_IDENTITY", "PANDORA_PDND_DIST_DIR",
		"AEGIS_GEOIP_DB", "AEGIS_GEOIP_IPV6_DB", "PANDORA_NATIVE_RELEASE_VERSION",
		"PANDORA_NATIVE_ARTIFACT_AMD64_SHA256", "PANDORA_NATIVE_ARTIFACT_ARM64_SHA256", "AEGIS_BACKUP_WEBDAV_CONFIG"} {
		t.Setenv(k, "")
	}
	d := loadDeployment()
	if d.BackupDir != "/var/backups/aegispanel" || d.PdndDistDir != "/opt/aegispanel/pdnd-dist" ||
		d.BackupAgeIdentity != "" || d.GeoIPDB != "" || d.GeoIPIPv6DB != "" ||
		d.NativeReleaseVersion != "" || len(d.NativeArtifactSHA256) != 0 {
		t.Fatalf("defaults = %+v", d)
	}
	if d.AdminGeoIPDB() != "/opt/aegispanel/geoip/ip2region_v4.xdb" {
		t.Fatalf("admin GeoIP default = %q", d.AdminGeoIPDB())
	}
	if got := LoadBackupWebDAV().ConfigPath; got != "/etc/aegispanel/backup-webdav.json" {
		t.Fatalf("webdav config default = %q", got)
	}

	t.Setenv("AEGIS_BACKUP_DIR", "  /srv/backups  ")
	t.Setenv("AEGIS_GEOIP_DB", "/srv/geo/v4.xdb")
	t.Setenv("PANDORA_NATIVE_ARTIFACT_ARM64_SHA256", " ABCDEF ")
	t.Setenv("PANDORA_NATIVE_RELEASE_VERSION", " pandora-native-test ")
	t.Setenv("AEGIS_BACKUP_WEBDAV_CONFIG", "/srv/webdav.json")
	d = loadDeployment()
	if d.BackupDir != "/srv/backups" || d.AdminGeoIPDB() != "/srv/geo/v4.xdb" ||
		d.NativeReleaseVersion != "pandora-native-test" || d.NativeArtifactSHA256["arm64"] != "abcdef" ||
		d.NativeArtifactSHA256["amd64"] != "" || LoadBackupWebDAV().ConfigPath != "/srv/webdav.json" {
		t.Fatalf("overrides = %+v", d)
	}
}
