package core

// 入站证书的共用约定：编排层（node/）、数据面（kernel/）与证书管理（certs/）三方都只认这里的类型，
// 互相不 import。配置文档的键见 .claude/rules/pdnd-node-panel.md「节点配置文档里节点端读取的键」。

import (
	"crypto/tls"
	"net"
	"strings"
	"time"
)

// 入站证书来源（配置文档的 cert_mode）：acme 由节点端自己向 CA 申请（pdnd/certs），
// file 读节点机上的 cert_path / key_path（缺省，没有 cert_mode 的配置一律按 file）。
const (
	CertModeACME = "acme"
	CertModeFile = "file"
)

// 证书相关的机器可读原因码：心跳 NodeRuntime.reason 与配置回执 reason_code。
const (
	// ReasonCertPending：acme 入站的证书还没申请到，入站没装（旧配置在的话继续服务）。
	ReasonCertPending = "cert_pending"
	// ReasonCertExpired：入站在服务，但证书已过期（续期一直失败），客户端会校验失败。
	ReasonCertExpired = "cert_expired"
)

// InboundCertMode 返回入站配置的证书来源：cert_mode 是 acme（不分大小写、去首尾空白）时为
// CertModeACME，其余（含缺省、非字符串）一律 CertModeFile。
func InboundCertMode(raw map[string]any) string {
	if mode, ok := raw["cert_mode"].(string); ok && strings.EqualFold(strings.TrimSpace(mode), CertModeACME) {
		return CertModeACME
	}
	return CertModeFile
}

// InboundCertIdentifier 返回 acme 入站的证书标识符（cert_identifier），按 NormalizeCertIdentifier 规整；
// 没有或不是字符串返回空串。
func InboundCertIdentifier(raw map[string]any) string {
	id, _ := raw["cert_identifier"].(string)
	return NormalizeCertIdentifier(id)
}

// NormalizeCertIdentifier 规整证书标识符：去首尾空白与 IPv6 的方括号；IP 取规范写法；
// 域名转小写、去掉结尾的点。空串原样返回。面板下发的已是规整形式，这里只为两端口径一致。
func NormalizeCertIdentifier(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimSuffix(strings.TrimPrefix(id, "["), "]")
	if ip := net.ParseIP(id); ip != nil {
		return ip.String()
	}
	return strings.TrimSuffix(strings.ToLower(id), ".")
}

// CertSource 按标识符给出自动申请到的证书（pdnd/certs.Manager 实现）。
// 返回的证书只读、不得修改；已过期的也照常返回（由调用方决定怎么报）；没有返回 false。
// 每次 TLS 握手都可能调用，实现必须并发安全且不阻塞（原子读）。
type CertSource interface {
	Certificate(identifier string) (*tls.Certificate, bool)
}

// CertStatus 是一个标识符的证书现状，给心跳 NodeRuntime.cert。
type CertStatus struct {
	Identifier string
	// NotAfter 是当前证书的到期时间；零值表示还没有证书
	NotAfter time.Time
	// ErrorCode / ErrorDetail 是最近一次申请或续期失败的原因码与详情；成功后清空
	ErrorCode   string
	ErrorDetail string
	// NextAttempt 是下一次申请或续期的计划时间；零值表示没有计划
	NextAttempt time.Time
}

// CertPendingError 表示 acme 入站的证书还没申请到。编排层在装入站之前就返回它（不碰内核、旧配置继续服务），
// 内核在没有证书时也返回它（兜底）。RuntimeReason 让心跳与配置回执报 cert_pending。
type CertPendingError struct {
	Identifier string
	// Detail 是证书管理报的最近一次失败（原因码与详情），可以为空
	Detail string
}

func (e *CertPendingError) Error() string {
	msg := "等待自动申请的证书（" + e.Identifier + "）"
	if e.Detail != "" {
		msg += "：" + e.Detail
	}
	return msg
}

// RuntimeReason 是心跳与配置回执里的原因码。
func (e *CertPendingError) RuntimeReason() string { return ReasonCertPending }
