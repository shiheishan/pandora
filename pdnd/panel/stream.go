package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// 面板事件流（SSE）。
//
// 轮询已经把「配置没变」压到一次 304，但压不掉延迟：改完配置最多要等一个
// 周期才生效。这条流把等待去掉。
//
// # 它是加速，不是依赖
//
// 连不上、断了、消息丢了，节点端都还有轮询兜底。所以这里所有失败路径都
// 只是记个日志然后退避重连，从不向上冒泡成错误——一条辅助通路不该让主
// 流程停摆。反过来说，也不能因为流连上了就把轮询停掉：那样流一断，节点
// 就彻底聋了，而 SSE 断连未必有明显信号。

// StreamEvent 是从面板收到的一条事件。
type StreamEvent struct {
	Type string
	// Users 在全量事件里是完整列表。
	Users []core.User
	// Version 是这份用户列表的版本，断线重连时报给面板。
	Version string
	// Added / Removed 在增量事件里有值。
	Added   []core.User
	Removed []int64
	// FromVersion 是这条增量基于哪一版。对不上就该丢弃并拉全量——
	// 错位地打补丁比不打更糟，会留下一批本该删掉的用户还在放行。
	FromVersion string
	ToVersion   string
	// ConfigETag 在配置变更事件里有值。
	ConfigETag string
}

// 事件类型，与面板侧一致。
const (
	EventSyncConfig    = "sync.config"
	EventSyncUsers     = "sync.users"
	EventSyncUserDelta = "sync.user.delta"
)

type wireMessage struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data,omitempty"`
}

type wireUsers struct {
	Users   []wireUser `json:"users"`
	Version string     `json:"version"`
}

type wireDelta struct {
	Delta struct {
		Added   []wireUser `json:"added"`
		Removed []int64    `json:"removed"`
	} `json:"delta"`
	FromVersion string `json:"from_version"`
	ToVersion   string `json:"to_version"`
}

type wireConfig struct {
	ETag string `json:"etag"`
}

// streamHealthyAfter 是一条连接「健康」所需的最短存活时间：面板每 20 秒发一次
// 心跳，活过两个心跳才算。原先读到一行就算健康，而面板每次连上都先推一行全量
// 用户——反代读超时小于 20 秒、面板崩溃循环、hub 溢出踢连接时，节点每次都
// 「健康地」断开、退避永远复位到 1 秒，实测每节点每分钟重连约 48 次。
const streamHealthyAfter = 40 * time.Second

// streamIdleTimeout 是读空闲上限：每读到一行续一次。面板活着但不说话（假死、
// 中间设备半开）时，没有它连接会永远挂着，推送加速静默失效。
const streamIdleTimeout = 60 * time.Second

// StreamUsersVersionHeader 是建事件流时报告「手上的用户名单是哪一版」的请求头，值与
// UniProxy /user 的 ETag 同源。面板据此跳过首个全量；不认识它的面板忽略即可。
const StreamUsersVersionHeader = "X-Users-Version"

// ErrStreamUnsupported 表示面板不提供事件流（第三方面板回 404），Stream 已停止，
// 节点只走轮询。
var ErrStreamUnsupported = errors.New("面板不支持事件流（HTTP 404），改为只走轮询")

// Stream 连上面板的事件流，把收到的事件送进 out，直到 ctx 结束。
//
// 自己负责重连，不返回错误——调用方起一个 goroutine 跑它就行。唯一的例外是
// 面板回 404：对接 Xboard 这类不支持事件流的面板时再连也是 404，onError 收到
// ErrStreamUnsupported 后就此停止，轮询照旧。
func (c *Client) Stream(ctx context.Context, out chan<- StreamEvent, onError func(error)) {
	// 退避从 1 秒起，翻倍到 30 秒封顶。加随机抖动：面板重启时几十个节点
	// 会同时断线，不抖的话它们会踩着同一个节拍一起重连，把刚起来的面板
	// 再打一遍。
	//
	// 一次健康的连接之后退避回到 1 秒。不复位的话面板重启过几次就封顶在
	// 30 秒，此后哪怕连接已经稳定挂了几天，下次断线也要等 30～45 秒才
	// 重连。「健康」的口径见 streamOnce：读到过帧、并且活过 streamHealthyAfter。
	const minBackoff, maxBackoff = time.Second, 30 * time.Second
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}
		healthy, err := c.streamOnce(ctx, out)
		if ctx.Err() != nil {
			return
		}
		var status *StatusError
		if errors.As(err, &status) && status.Code == http.StatusNotFound {
			if onError != nil {
				onError(ErrStreamUnsupported)
			}
			return
		}
		if err != nil && onError != nil {
			onError(err)
		}
		if healthy {
			backoff = minBackoff
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		if !c.waitReconnect(ctx, backoff+jitter) {
			return
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// waitReconnect 等 d 再重连，ctx 先结束则返回 false。
func (c *Client) waitReconnect(ctx context.Context, d time.Duration) bool {
	if c.streamWait != nil {
		return c.streamWait(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) streamClock() time.Time {
	if c.streamNow != nil {
		return c.streamNow()
	}
	return time.Now()
}

// streamOnce 建一次连接，读到断开为止。
//
// healthy 表示这条连接至少完整读到过一行（事件或心跳注释），并且从发起到断开
// 活过了 streamHealthyAfter。心跳也算一行：空闲的面板只发心跳，那同样是一条
// 正常工作的流。只回 200 不算，读到一行就断也不算：「接了就断」「回一行就断」
// 的上游会让退避永远停在最低档，每秒一次地敲同一扇门。
func (c *Client) streamOnce(ctx context.Context, out chan<- StreamEvent) (healthy bool, err error) {
	started := c.streamClock()
	var gotLine bool
	defer func() {
		healthy = gotLine && c.streamClock().Sub(started) >= streamHealthyAfter
	}()

	// 读期限：响应头与之后每一行都要在 idle 之内到达，否则掐断这条连接。
	// 用取消 context 实现，读阻塞在 Body.Read 上也能被打断。
	idle := c.streamIdle
	if idle <= 0 {
		idle = streamIdleTimeout
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	idleTimer := time.AfterFunc(idle, cancel)
	defer idleTimer.Stop()

	req, err := c.newRequest(streamCtx, http.MethodGet, "stream", nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "text/event-stream")
	// 报上手上的用户名单版本：面板发现与当前版一致就不再推首个全量（面板网关重启后
	// 成百条流同时重连，原先每条都要推一份全量）。值取客户端记着的用户 ETag——它与
	// 内核里的名单同生同灭（见 node 的 resetUserMirror），装不上就会被作废。
	if v := UsersVersionKey(c.UsersVersion()); v != "" {
		req.Header.Set(StreamUsersVersionHeader, v)
	}

	// 事件流要挂很久，不能用带总超时的那个 http.Client——它会在
	// Timeout 到点时把连接掐掉，表现为每隔固定时间断一次。
	client := &http.Client{Transport: c.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return false, idleError(ctx, streamCtx, idle, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, httpError("连接事件流", resp)
	}

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return false, nil // 面板正常关闭了流
			}
			return false, idleError(ctx, streamCtx, idle, err)
		}
		gotLine = true
		idleTimer.Reset(idle)
		line = strings.TrimRight(line, "\r\n")
		// 空行是事件分隔，冒号开头是注释（心跳），都跳过
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		event, ok := parseStreamEvent([]byte(payload))
		if !ok {
			continue
		}
		select {
		case out <- event:
		case <-ctx.Done():
			return false, nil
		}
	}
}

// idleError 把「读期限到点、连接被掐」与其它读错误区分开，日志里看得出是面板不说话。
func idleError(parent, stream context.Context, idle time.Duration, err error) error {
	if parent.Err() == nil && stream.Err() != nil {
		return fmt.Errorf("事件流 %s 内没有任何数据，已断开重连: %w", idle, err)
	}
	return err
}

// parseStreamEvent 把一条 data 行解析成事件。
//
// 解析不了就跳过而不是断开：面板加了新事件类型时，老节点端应当忽略它
// 继续工作，而不是陷入「连上－读到不认识的－断开－重连」的循环。
func parseStreamEvent(raw []byte) (StreamEvent, bool) {
	var msg wireMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return StreamEvent{}, false
	}
	e := StreamEvent{Type: msg.Event}
	switch msg.Event {
	case EventSyncUsers:
		var p wireUsers
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return StreamEvent{}, false
		}
		e.Users = toCoreUsers(p.Users)
		e.Version = p.Version
	case EventSyncUserDelta:
		var p wireDelta
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return StreamEvent{}, false
		}
		e.Added = toCoreUsers(p.Delta.Added)
		e.Removed = p.Delta.Removed
		e.FromVersion, e.ToVersion = p.FromVersion, p.ToVersion
	case EventSyncConfig:
		var p wireConfig
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return StreamEvent{}, false
		}
		e.ConfigETag = p.ETag
	default:
		return StreamEvent{}, false
	}
	return e, true
}

func toCoreUsers(in []wireUser) []core.User {
	out := make([]core.User, 0, len(in))
	for _, u := range in {
		out = append(out, core.User{
			ID: u.ID, UUID: u.UUID,
			SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit,
		})
	}
	return out
}
