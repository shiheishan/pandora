// [INPUT]: 依赖 connerror.go 的 connErrorReporter，依赖 sing 的 logger.Logger 接口，依赖 sagernet/quic-go 的 ApplicationError / IdleTimeoutError
// [OUTPUT]: 包内提供 newSingConnErrorLogger（把 sing 系服务端的 Error 级日志转成 ConnError）
// [POS]: kernel 连接失败观测链给 QUIC 协议的旁路：tuic.go、hysteria2.go 把它作为 nativewire 服务的 Logger，鉴权失败与会话异常就从上游库内部浮出来

package kernel

import (
	"errors"
	"fmt"

	"github.com/sagernet/quic-go"
	"github.com/sagernet/sing/common/logger"
)

// singConnErrorLogger 实现 sing 的 logger.Logger，只接 Error 及以上。
//
// TUIC 的 UUID / token 校验、Hysteria2 的流请求解析都在 nativewire 的服务端
// 内部完成，失败时上游库只会调一次 logger.Error 然后关连接——原来传的是
// logger.NOP()，于是「TUIC 连不上」在节点端没有任何痕迹。桥接到 ConnError
// 后，这些失败与其他协议走同一套分类、脱敏与限流；Error 以下的级别（含
// 上游自己判定为正常关闭的 Debug）照旧丢弃。
type singConnErrorLogger struct {
	logger.Logger
	report connErrorReporter
	// mark 可选：按协议给错误补分类标记（例如 TUIC 的 unknown user 归 auth）。
	mark func(error) error
}

func newSingConnErrorLogger(report connErrorReporter, mark func(error) error) logger.Logger {
	return singConnErrorLogger{Logger: logger.NOP(), report: report, mark: mark}
}

func (l singConnErrorLogger) Error(args ...any) { l.forward(args) }
func (l singConnErrorLogger) Fatal(args ...any) { l.forward(args) }
func (l singConnErrorLogger) Panic(args ...any) { l.forward(args) }

func (l singConnErrorLogger) forward(args []any) {
	var err error
	if len(args) == 1 {
		err, _ = args[0].(error)
	}
	if err == nil {
		err = errors.New(fmt.Sprint(args...))
	}
	if isQUICNormalClose(err) {
		return
	}
	if l.mark != nil {
		err = l.mark(err)
	}
	// 上游日志不带对端地址；QUIC 会话也没有可上报的 net.Conn。
	l.report.addr(StageSession, nil, err)
}

// isQUICNormalClose 认出对端正常离开的 QUIC 关闭。
//
// TUIC 服务端在客户端断开后还会再发一次心跳，失败就记一条 Error——不滤掉
// 的话，每个正常结束的 TUIC 会话都会被当成失败记一笔。错误码 0 的应用层
// 关闭与空闲超时都是正常收尾。
func isQUICNormalClose(err error) bool {
	var app *quic.ApplicationError
	if errors.As(err, &app) && app.ErrorCode == 0 {
		return true
	}
	var idle *quic.IdleTimeoutError
	return errors.As(err, &idle)
}
