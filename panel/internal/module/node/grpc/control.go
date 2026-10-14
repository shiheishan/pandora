package grpc

// ControlService 的实现：发送循环在单独 goroutine，接收循环在另一 goroutine。
// 业务都在 service 的会话管理（service/session*.go）；这里只做收发、proto 互转与状态码。
// handler 在 sess.Done() 后最多再等 sessionEndGrace 让 Tombstone / reconnect 发出去，然后返回；
// 身份失效（PermissionDenied）时附带 TrailerReenroll。

import (
	"context"
	"errors"
	"math/big"
	"time"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aegispanel/aegis/internal/module/node/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
)

// sessionEndGrace 是会话结束后再等多久让最后一条（Tombstone / reconnect）发出去；
// 过了就返回、取消流 ctx，卡在 Send 上的发送循环随之解除。
const sessionEndGrace = 2 * time.Second

type controlServer struct {
	nodev1.UnimplementedControlServiceServer
	svc  *service.Service
	auth sessionAuthorizer
}

func newControlServer(svc *service.Service, auth sessionAuthorizer) *controlServer {
	return &controlServer{svc: svc, auth: auth}
}

// NewControlServer 构造 ControlService 的服务端实现；server.go 注册它。会话管理器每次建流时经
// svc.SessionManager() 取（装配时 svc.UseSessions(svc.NewSessions(...))），没装配时建流回 Unavailable。
func NewControlServer(svc *service.Service) nodev1.ControlServiceServer {
	c := &controlServer{svc: svc}
	if svc != nil {
		c.auth = svc
	}
	return c
}

// Session 处理一条控制流：先收 Hello、登记会话（取代同一服务器的旧流），登记后复核身份，
// 发送循环在单独 goroutine、接收循环在另一个；handler 在发送结束或 sess.Done() 加宽限后返回。
func (c *controlServer) Session(stream nodev1.ControlService_SessionServer) error {
	ctx := stream.Context()
	serverID, ok := ServerIDFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "未认证的节点")
	}
	serial, ok := CertSerialFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "未认证的节点")
	}
	ctx = withStreamLogger(ctx)
	log := plog.From(ctx)

	var m *service.Sessions
	if c.svc != nil {
		m = c.svc.SessionManager()
	}
	if m == nil {
		log.Error("会话管理器未装配，拒绝建流")
		return status.Error(codes.Unavailable, "面板暂不可用")
	}

	if c.auth == nil {
		log.Error("身份复核未装配，拒绝建流")
		return status.Error(codes.Unavailable, "面板暂不可用")
	}

	hello, err := recvHello(stream)
	if err != nil {
		return err
	}
	// 拦截器的鉴权可能是很久之前做的（流建好后迟迟不发 Hello），登记前后各复核一次，副作用都落在复核之后
	verify := func(ctx context.Context) error { return c.auth.AuthorizeSession(ctx, serverID, serial) }
	sess, err := m.OpenVerified(ctx, serverID, helloFromProto(hello), verify)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrSessionRevoked):
			log.Warn("建流复核发现身份已失效，拒绝")
			stream.SetTrailer(metadata.Pairs(TrailerReenroll, "required"))
			return status.Error(codes.PermissionDenied, "接入身份已失效，需要在后台生成新令牌重新接入")
		case errors.Is(err, service.ErrSessionShutdown):
			return status.Error(codes.Unavailable, "面板正在停止")
		case errors.Is(err, service.ErrAlignBusy):
			log.Warn("建流对齐超时，请节点稍后重连")
			return status.Error(codes.Unavailable, "面板繁忙，稍后重连")
		}
		log.Error("建立节点会话失败", zap.Error(err))
		return status.Error(codes.Unavailable, "面板暂不可用")
	}
	defer sess.Close(nil)

	sendDone := make(chan error, 1)
	go func() { sendDone <- sendLoop(ctx, stream, sess) }()
	go recvLoop(ctx, stream, sess, serial)

	select {
	case err = <-sendDone:
	case <-sess.Done():
		t := time.NewTimer(sessionEndGrace)
		select {
		case err = <-sendDone:
		case <-t.C:
			err = sessionStatus(sess.Err())
		}
		t.Stop()
	}

	if status.Code(err) == codes.PermissionDenied {
		stream.SetTrailer(metadata.Pairs(TrailerReenroll, "required"))
	}
	return err
}

// helloTimeout 是建流后等第一条 Hello 的时限：拦截器的鉴权只在建流时做一次，不能让一条没发 Hello 的流无限期挂着。
const helloTimeout = 10 * time.Second

// recvHello 在 helloTimeout 内收第一条消息，它必须是 Hello。超时时 handler 返回会取消流，阻塞的 Recv 随之返回。
func recvHello(stream nodev1.ControlService_SessionServer) (*nodev1.Hello, error) {
	type recvd struct {
		msg *nodev1.SessionRequest
		err error
	}
	ch := make(chan recvd, 1)
	go func() {
		msg, err := stream.Recv()
		ch <- recvd{msg, err}
	}()
	t := time.NewTimer(helloTimeout)
	defer t.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if h := r.msg.GetHello(); h != nil {
			return h, nil
		}
		return nil, status.Error(codes.InvalidArgument, "流上第一条消息必须是 Hello")
	case <-t.C:
		return nil, status.Error(codes.DeadlineExceeded, "建流后没有按时发 Hello")
	}
}

// sendLoop 从会话取下一条要发的消息并写入流；只能由一个 goroutine 调用。
func sendLoop(ctx context.Context, stream nodev1.ControlService_SessionServer, sess *service.Session) error {
	var seq uint64
	for {
		out, err := sess.Next(ctx)
		if err != nil {
			return sessionStatus(err)
		}
		for _, msg := range outboundToProto(out) {
			seq++
			msg.Seq = seq
			if err := stream.Send(msg); err != nil {
				sess.Close(service.ErrSessionPeerGone)
				return err
			}
		}
	}
}

// recvLoop 逐条处理节点发来的消息；Recv 出错（节点断开、流结束）时结束会话，唤醒发送循环。
// 会话已在结束时丢掉后续消息，不入账流量与在线。
func recvLoop(ctx context.Context, stream nodev1.ControlService_SessionServer, sess *service.Session, serial *big.Int) {
	log := plog.From(ctx)
	for {
		req, err := stream.Recv()
		if err != nil {
			sess.Close(service.ErrSessionPeerGone)
			return
		}
		if sess.Ending() {
			return
		}
		switch body := req.GetBody().(type) {
		case *nodev1.SessionRequest_Heartbeat:
			sess.HandleHeartbeat(ctx, heartbeatFromProto(body.Heartbeat))
		case *nodev1.SessionRequest_Traffic:
			r, ok := trafficFromProto(body.Traffic)
			if !ok {
				// 编号不合法是节点端 bug：记 ERROR、照样回 ack，免得节点无限重发
				log.Error("流量上报编号不合法，丢弃并回 ack", zap.Uint64("seq", req.GetSeq()))
				sess.AckTraffic(body.Traffic.GetReportId())
				continue
			}
			sess.HandleTraffic(ctx, r)
		case *nodev1.SessionRequest_Online:
			sess.HandleOnline(ctx, onlineFromProto(body.Online))
		case *nodev1.SessionRequest_ConfigAck:
			a, ok := configAckFromProto(body.ConfigAck)
			if !ok {
				log.Warn("配置回执不合法，忽略", zap.Uint64("seq", req.GetSeq()))
				continue
			}
			sess.HandleConfigAck(ctx, a)
		case *nodev1.SessionRequest_UsersAck:
			a, ok := usersAckFromProto(body.UsersAck)
			if !ok {
				log.Warn("名单回执不合法，忽略", zap.Uint64("seq", req.GetSeq()))
				continue
			}
			sess.HandleUsersAck(a)
		case *nodev1.SessionRequest_Renew:
			sess.HandleRenew(ctx, serial, body.Renew.GetCsrDer())
		case *nodev1.SessionRequest_Hello:
			log.Warn("流中途又收到 Hello，忽略", zap.Uint64("seq", req.GetSeq()))
		default:
			log.Warn("收到无法识别的消息，忽略", zap.Uint64("seq", req.GetSeq()))
		}
	}
}

// withStreamLogger 保证流的 ctx 带 req_id 与带 req_id 的 logger：拦截器已放好就原样返回，
// 没有就读 metadata 的 x-request-id（不合法则新生成）。server_id 只由鉴权拦截器挂到 logger 上（interceptors.go 的 authorize），
// 这里与会话层都不再加。
func withStreamLogger(ctx context.Context) context.Context {
	if reqid.From(ctx) != "" {
		return ctx
	}
	var id string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(reqid.MetadataKey); len(v) > 0 {
			id = v[0]
		}
	}
	return plog.WithReqID(ctx, plog.From(ctx), reqid.Normalize(id))
}

// sessionStatus 把会话结束的原因译成 gRPC 状态。
func sessionStatus(err error) error {
	switch {
	case errors.Is(err, service.ErrSessionSuperseded):
		return status.Error(codes.Aborted, "被同一服务器的新流取代")
	case errors.Is(err, service.ErrSessionTombstoned):
		return status.Error(codes.PermissionDenied, "接入身份已失效，需要在后台生成新令牌重新接入")
	case errors.Is(err, service.ErrSessionOverflow):
		return status.Error(codes.ResourceExhausted, "发送队列已满，请重连")
	case errors.Is(err, service.ErrSessionShutdown):
		return status.Error(codes.Unavailable, "面板正在停止")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return status.FromContextError(err).Err()
	default:
		return status.Error(codes.Canceled, "会话已结束")
	}
}
