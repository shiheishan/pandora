package certs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-acme/lego/v5/acme"
)

// 原因码：心跳 CertStatus.error_code 的取值，面板后台有一张同名的中文表（两端一起加）。
const (
	ReasonChallengePortBusy  = "challenge_port_busy" // 本机 80 端口（HTTP-01）被别的程序占着，没有下单
	ReasonValidationFailed   = "validation_failed"   // CA 访问不到本机 80 端口或应答不对（防火墙、解析不到本机）
	ReasonDNSFailed          = "dns_failed"          // CA 解析域名失败
	ReasonCAAForbidden       = "caa_forbidden"       // CAA 记录不允许该 CA
	ReasonRejectedIdentifier = "rejected_identifier" // CA 拒绝这个名字（内网 IP、保留域名、黑名单）
	ReasonRateLimited        = "rate_limited"        // 触发 CA 限速
	ReasonPaused             = "paused"              // CA 因连续失败暂停了这个名字
	ReasonProfileUnsupported = "profile_unsupported" // CA 不支持所需的档位（shortlived）
	ReasonACMEUnreachable    = "acme_unreachable"    // 连不上 ACME 目录或超时
	ReasonAccountFailed      = "account_failed"      // 注册 ACME 账户失败
	ReasonStoreFailed        = "store_failed"        // 写盘失败
	ReasonUnknown            = "unknown"
)

const maxDetailBytes = 1024

// issueError 是带原因码的失败（Manager 自己构造：端口占用、写盘失败、账户失败）。
type issueError struct {
	code       string
	err        error
	retryAfter time.Duration
}

func (e *issueError) Error() string {
	if e.err == nil {
		return e.code
	}
	return fmt.Sprintf("%s: %v", e.code, e.err)
}

func (e *issueError) Unwrap() error { return e.err }

// classify 把一次申请失败归成原因码、详情与（限速时的）Retry-After。
func classify(err error) (code, detail string, retryAfter time.Duration) {
	if err == nil {
		return ReasonUnknown, "", 0
	}

	var ie *issueError
	if errors.As(err, &ie) {
		d := ""
		if ie.err != nil {
			d = cleanDetail(ie.err.Error())
		}
		return ie.code, d, ie.retryAfter
	}

	var rate *acme.RateLimitedError
	if errors.As(err, &rate) {
		return classifyRateLimited(rate.ProblemDetails, rate.RetryAfter)
	}

	var prob *acme.ProblemDetails
	if errors.As(err, &prob) {
		return classifyProblemDetails(prob)
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonACMEUnreachable, cleanDetail(err.Error()), 0
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return ReasonACMEUnreachable, cleanDetail(err.Error()), 0
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return ReasonACMEUnreachable, cleanDetail(err.Error()), 0
	}

	return ReasonUnknown, cleanDetail(err.Error()), 0
}

func classifyRateLimited(prob *acme.ProblemDetails, retryAfter time.Duration) (string, string, time.Duration) {
	detail := problemDetailText(prob)
	if strings.Contains(strings.ToLower(detail), "paused") {
		return ReasonPaused, cleanDetail(detail), retryAfter
	}
	return ReasonRateLimited, cleanDetail(detail), retryAfter
}

func classifyProblemDetails(prob *acme.ProblemDetails) (string, string, time.Duration) {
	typeStr := prob.Type
	detail := prob.Detail
	if len(prob.SubProblems) > 0 {
		sub := prob.SubProblems[0]
		typeStr = sub.Type
		if sub.Detail != "" {
			if detail != "" {
				detail = detail + "; " + sub.Detail
			} else {
				detail = sub.Detail
			}
		}
	}
	kind := acmeErrorKind(typeStr)
	switch kind {
	case "rateLimited":
		return classifyRateLimited(prob, 0)
	case "caa":
		return ReasonCAAForbidden, cleanDetail(detail), 0
	case "dns":
		return ReasonDNSFailed, cleanDetail(detail), 0
	case "connection", "unauthorized", "incorrectResponse", "tls":
		return ReasonValidationFailed, cleanDetail(detail), 0
	case "rejectedIdentifier", "unsupportedIdentifier":
		return ReasonRejectedIdentifier, cleanDetail(detail), 0
	case "invalidProfile":
		return ReasonProfileUnsupported, cleanDetail(detail), 0
	default:
		return ReasonUnknown, cleanDetail(detail), 0
	}
}

func problemDetailText(prob *acme.ProblemDetails) string {
	if prob == nil {
		return ""
	}
	detail := prob.Detail
	if len(prob.SubProblems) > 0 && prob.SubProblems[0].Detail != "" {
		if detail != "" {
			detail = detail + "; " + prob.SubProblems[0].Detail
		} else {
			detail = prob.SubProblems[0].Detail
		}
	}
	return detail
}

func acmeErrorKind(typeURN string) string {
	if i := strings.LastIndex(typeURN, ":"); i >= 0 && i+1 < len(typeURN) {
		return typeURN[i+1:]
	}
	return typeURN
}

// cleanDetail 只留可打印 ASCII（0x20–0x7e），截到 maxDetailBytes。
func cleanDetail(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > maxDetailBytes {
		out = out[:maxDetailBytes]
	}
	return out
}
