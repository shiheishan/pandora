// [INPUT]: 依赖 config.go 的 env 读取助手与进程环境变量
// [OUTPUT]: 对外提供 Deployment（Config 内嵌的部署路径、GeoIP、销售授权、NativeCore 发布绑定）、BackupWebDAV 与 LoadBackupWebDAV、DefaultBackupWebDAVConfigPath
// [POS]: platform/config 的部署侧配置：全是「跟着发布产物与主机走、运营不改」的项，都可缺省，从不让 Load 多出必填项；独立小二进制用各自的小加载函数
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package config

import "strings"

//------------------------------------------------------------------------------
// 部署项：由 Load 一并读入，缺省即用默认值，不参与「缺一项拒绝启动」
//------------------------------------------------------------------------------

const (
	defaultBackupDir   = "/var/backups/aegispanel"
	defaultPdndDistDir = "/opt/aegispanel/pdnd-dist"
	// defaultAdminGeoIPDB 只是 aegis-admin 的缺省位置；aegis-node 历来没有缺省，
	// 未设置就不开 GeoIP（见 AdminGeoIPDB 与 GeoIPDB 的区别）。
	defaultAdminGeoIPDB = "/opt/aegispanel/geoip/ip2region_v4.xdb"

	// DefaultBackupWebDAVConfigPath 是异地备份配置文件的缺省位置。aegis-admin 的
	// 系统状态页也拿它判断「异地备份配了没有」。
	DefaultBackupWebDAVConfigPath = "/etc/aegispanel/backup-webdav.json"
)

// Deployment 是部署决定、运营不改的配置。它们为什么走环境变量而不进配置表：
// 路径跟着发布产物走，销售授权与 NativeCore 绑定必须在进程启动时定死，任何请求
// 都改不了。
type Deployment struct {
	// BackupDir 是备份产物目录，系统状态页直接看这里的文件（AEGIS_BACKUP_DIR）。
	BackupDir string
	// BackupAgeIdentity 是备份解密私钥的路径，面板只判断配没配、文件在不在，
	// 从不读内容（AEGIS_BACKUP_AGE_IDENTITY）。
	BackupAgeIdentity string
	// PdndDistDir 是 pdnd 一键安装分发的二进制目录（PANDORA_PDND_DIST_DIR）。
	PdndDistDir string
	// GeoIPDB 是 AEGIS_GEOIP_DB 的原值（去首尾空白，未设置为空）。
	GeoIPDB string
	// GeoIPIPv6DB 是可选的 IPv6 库（AEGIS_GEOIP_IPV6_DB），空则只查 IPv4。
	GeoIPIPv6DB string
	// SalesEnabled 是部署方对定价与上架的显式授权（AEGIS_SALES_ENABLED）：
	// 只认 1 / true / yes，其余一律未授权，含糊按拒绝处理。
	SalesEnabled bool
	// NativeArtifactSHA256 是本次发布钉死的 NativeCore 二进制摘要，键为架构
	// amd64 / arm64，值为小写十六进制，未设置的架构不出现
	// （PANDORA_NATIVE_ARTIFACT_<ARCH>_SHA256）。
	NativeArtifactSHA256 map[string]string
	// NativeReleaseVersion 是本次发布的 NativeCore 版本（PANDORA_NATIVE_RELEASE_VERSION）。
	// 生产缺失摘要或版本时节点接入 fail closed，由 nodefabric 判定。
	NativeReleaseVersion string
}

// nativeArchitectures 是节点接入接受的架构，与 nodefabric 的接入证据校验一致。
var nativeArchitectures = []string{"amd64", "arm64"}

func loadDeployment() Deployment {
	d := Deployment{
		BackupDir:            trimmedEnv("AEGIS_BACKUP_DIR", defaultBackupDir),
		BackupAgeIdentity:    trimmedEnv("AEGIS_BACKUP_AGE_IDENTITY", ""),
		PdndDistDir:          trimmedEnv("PANDORA_PDND_DIST_DIR", defaultPdndDistDir),
		GeoIPDB:              trimmedEnv("AEGIS_GEOIP_DB", ""),
		GeoIPIPv6DB:          trimmedEnv("AEGIS_GEOIP_IPV6_DB", ""),
		NativeArtifactSHA256: map[string]string{},
		NativeReleaseVersion: trimmedEnv("PANDORA_NATIVE_RELEASE_VERSION", ""),
	}
	switch strings.ToLower(trimmedEnv("AEGIS_SALES_ENABLED", "")) {
	case "1", "true", "yes":
		d.SalesEnabled = true
	}
	for _, arch := range nativeArchitectures {
		name := "PANDORA_NATIVE_ARTIFACT_" + strings.ToUpper(arch) + "_SHA256"
		if digest := strings.ToLower(trimmedEnv(name, "")); digest != "" {
			d.NativeArtifactSHA256[arch] = digest
		}
	}
	return d
}

// AdminGeoIPDB 是 aegis-admin 打开 GeoIP 的路径：未设置时用发布包里的缺省位置。
func (d Deployment) AdminGeoIPDB() string {
	if d.GeoIPDB != "" {
		return d.GeoIPDB
	}
	return defaultAdminGeoIPDB
}

//------------------------------------------------------------------------------
// 独立小二进制：aegis-backup-webdav 不连数据库，不走 Load
//------------------------------------------------------------------------------

// BackupWebDAV 是 aegis-backup-webdav 的全部环境配置。
type BackupWebDAV struct {
	// ConfigPath 是 WebDAV 目标与签名密钥位置的 JSON 文件（AEGIS_BACKUP_WEBDAV_CONFIG）。
	ConfigPath string
}

// LoadBackupWebDAV 只读异地备份需要的那一项，不要求数据库与主密钥。
func LoadBackupWebDAV() BackupWebDAV {
	return BackupWebDAV{ConfigPath: trimmedEnv("AEGIS_BACKUP_WEBDAV_CONFIG", DefaultBackupWebDAVConfigPath)}
}

// trimmedEnv 读去掉首尾空白的值，空（含全空白）时用 def。
func trimmedEnv(k, def string) string {
	if v := strings.TrimSpace(env(k, "")); v != "" {
		return v
	}
	return def
}
