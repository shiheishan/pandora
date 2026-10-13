package grpc

// ControlService 的实现（p5-session）：一条流一个发送循环（本 goroutine）加一个接收循环。
// 业务都在 service 的会话管理（service/session*.go）；这里只做收发、proto 互转与状态码。

import (
	"context"
	"errors"

	nodev1 "github.com/aegispanel/nodeapi/pandora/node/v1"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/aegispanel/aegis/internal/module/node/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
)

type controlServer struct {
	nodev1.UnimplementedControlServiceServer
	svc *service.Service
}

// NewControlServer 构造 ControlService 的服务端实现；server.go 注册它。会话管理器每次建流时经
// svc.SessionManager() 取（装配时 svc.UseSessions(svc.NewSessions(...))），没装配时建流回 Unavailable。
func NewControlServer(svc *service.Service) nodev1.ControlServiceServer {
	return &controlServer{svc: svc}
}

// Session 处理一条控制流：先收 Hello、登记会话（取代同一服务器的旧流），之后发送循环在本 goroutine、
// 接收循环在另一个 goroutine；任一方结束都结束会话并移出登记。
func (c *controlServer) Session(stream nodev1.ControlService_SessionServer) error {
	ctx := stream.Context()
	serverID, ok := ServerIDFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "未认证的节点")
	}
	ctx = withStreamLogger(ctx, serverID.String())
	log := plog.From(ctx)

	var m *service.Sessions
	if c.svc != nil {
		m = c.svc.SessionManager()
	}
	if m == nil {
		log.Error("会话管理器未装配，拒绝建流")
		return status.Error(codes.Unavailable, "面板暂不可用")
	}

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "流上第一条消息必须是 Hello")
	}
	sess, err := m.Open(ctx, serverID, helloFromProto(hello))
	if err != nil {
		if errors.Is(err, service.ErrSessionShutdown) {
			return status.Error(codes.Unavailable, "面板正在停止")
		}
		log.Error("建立节点会话失败", zap.Error(err))
		return status.Error(codes.Unavailable, "面板暂不可用")
	}
	defer sess.Close(nil)

	go recvLoop(ctx, stream, sess)

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
func recvLoop(ctx context.Context, stream nodev1.ControlService_SessionServer, sess *service.Session) {
	log := plog.From(ctx)
	for {
		req, err := stream.Recv()
		if err != nil {
			sess.Close(service.ErrSessionPeerGone)
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
			sess.HandleRenew(ctx, body.Renew.GetCsrDer())
		case *nodev1.SessionRequest_Hello:
			log.Warn("流中途又收到 Hello，忽略", zap.Uint64("seq", req.GetSeq()))
		default:
			log.Warn("收到无法识别的消息，忽略", zap.Uint64("seq", req.GetSeq()))
		}
	}
}

// withStreamLogger 给流的 ctx 挂上带 req_id、server_id 的 logger。req_id 优先取拦截器放进 ctx 的，
// 没有就读 metadata 的 x-request-id（不合法则新生成）。
func withStreamLogger(ctx context.Context, serverID string) context.Context {
	if reqid.From(ctx) != "" {
		// 拦截器已放好 req_id 与带 req_id 的 logger
		return plog.With(ctx, plog.From(ctx).With(zap.String("server_id", serverID)))
	}
	var id string
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get(reqid.MetadataKey); len(v) > 0 {
			id = v[0]
		}
	}
	ctx = plog.WithReqID(ctx, plog.From(ctx), reqid.Normalize(id))
	return plog.With(ctx, plog.From(ctx).With(zap.String("server_id", serverID)))
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
