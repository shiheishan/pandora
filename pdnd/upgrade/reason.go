// Package upgrade 是节点在线升级：从面板接收签名发布清单、签名与下载地址，
// 并在节点端安全地下载、校验与更新二进制。
//
// 信任根是内置在 pdnd 二进制里的 ed25519 发版公钥。面板给的 URL、摘要与清单均不可信，
// 仅作为搬运工；节点进程（低权限 pandora 用户）与 root 侧助手各自独立做完整的验签、
// 架构核对与 serial 检查，确保即便面板被攻破也无法在节点上执行未经官方签名的代码。
package upgrade

import (
	"errors"
	"fmt"
	"regexp"
	"time"
)

// 失败原因码（与面板契约对齐，用于 UpgradeReport 及结果文件）。
const (
	ReasonDownloadFailed   = "download_failed"
	ReasonSHA256Mismatch   = "sha256_mismatch"
	ReasonSignatureInvalid = "signature_invalid"
	ReasonManifestInvalid  = "manifest_invalid"
	ReasonSerialNotNewer   = "serial_not_newer"
	ReasonBusy             = "busy"
	ReasonApplyFailed      = "apply_failed"
	ReasonConfirmTimeout   = "confirm_timeout"
	ReasonRollbackFailed   = "rollback_failed"
)

// 结果文件 result 里的 phase（与 proto UpgradePhase 的后缀同名，node 包换算）。
const (
	PhaseFailed     = "FAILED"
	PhaseRolledBack = "ROLLED_BACK"
)

// 固定路径：助手只认这些，不信 request 里的任何路径。
const (
	DefaultStateParent = "/var/lib/pandora-native"         // pandora 属主（systemd StateDirectory）
	StateSubdir        = "upgrade"                         // 节点进程写的暂存目录名
	DefaultStateDir    = "/var/lib/pandora-native/upgrade" // = DefaultStateParent + "/" + StateSubdir
	DefaultWorkDir     = "/var/lib/pandora-native-upgrade" // root 0700，助手自己的工作区
	DefaultBinPath     = "/usr/local/bin/pandora-native"
	DefaultPathUnit    = "/etc/systemd/system/pandora-native-upgrade.path"
	MainUnit           = "pandora-native.service"
)

// 目录里的文件名（StateDir 与 WorkDir 共用这些名字）。
const (
	FileRequest   = "request"
	FileNext      = "next"
	FileNextTmp   = "next.tmp"
	FileManifest  = "manifest.json"
	FileSignature = "manifest.json.sig"
	FileApplying  = "applying"
	FileHealthy   = "healthy"
	FileResult    = "result"
)

// 大小上限约束，防止恶意输入消耗内存或磁盘。
const (
	MaxBinaryBytes    int64 = 128 << 20
	MaxManifestBytes  int64 = 64 << 10
	MaxSignatureBytes int64 = 4 << 10
	MaxSmallFileBytes int64 = 4 << 10 // request、applying、healthy、result
)

// jobIDRegex 校验任务 ID 格式，限制为英文字母、数字和减号，长 1 到 64 字节。
var jobIDRegex = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// Error 是带原因码的结构化升级错误；Detail 是面向维护者的中文短句，不包含机密凭据。
type Error struct {
	Code   string
	Detail string
}

// Error 实现 error 接口。当 Detail 为空时仅输出 Code，否则输出 Code: Detail。
func (e *Error) Error() string {
	if e.Detail == "" {
		return e.Code
	}
	return e.Code + ": " + e.Detail
}

// Errorf 构造带原因码与格式化说明的 *Error。
func Errorf(code, format string, args ...any) *Error {
	return &Error{
		Code:   code,
		Detail: fmt.Sprintf(format, args...),
	}
}

// ReasonOf 提取错误链上的原因码与中文详情：
// - 若 err 为 nil，返回 ("", "")；
// - 若 err 链上存在 *Error，返回其 Code 与 Detail；
// - 否则返回 fallback 原因码与 err.Error()。
func ReasonOf(err error, fallback string) (code, detail string) {
	if err == nil {
		return "", ""
	}
	var upgradeErr *Error
	if errors.As(err, &upgradeErr) {
		return upgradeErr.Code, upgradeErr.Detail
	}
	return fallback, err.Error()
}

// ValidJobID 校验 job_id 是否合法（^[A-Za-z0-9-]{1,64}$，面板下发 UUID）。
func ValidJobID(s string) bool {
	return jobIDRegex.MatchString(s)
}

// ClampConfirm 规范化等待新进程连上面板的超时秒数：
// 0 映射为缺省 180 秒；小于 30 截断为 30 秒；大于 300 截断为 300 秒；其余保持原值。
func ClampConfirm(sec uint32) time.Duration {
	switch {
	case sec == 0:
		return 180 * time.Second
	case sec < 30:
		return 30 * time.Second
	case sec > 300:
		return 300 * time.Second
	default:
		return time.Duration(sec) * time.Second
	}
}
