package panel

import (
	"bufio"
	"context"
	"encoding/json"
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

// Stream 连上面板的事件流，把收到的事件送进 out，直到 ctx 结束。
//
// 自己负责重连，不返回错误——调用方起一个 goroutine 跑它就行。
func (c *Client) Stream(ctx context.Context, out chan<- StreamEvent, onError func(error)) {
	// 退避从 1 秒起，翻倍到 30 秒封顶。加随机抖动：面板重启时几十个节点
	// 会同时断线，不抖的话它们会踩着同一个节拍一起重连，把刚起来的面板
	// 再打一遍。
	const minBackoff, maxBackoff = time.Second, 30 * time.Second
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamOnce(ctx, out)
		if ctx.Err() != nil {
			return
		}
		if err != nil && onError != nil {
			onError(err)
		}
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// streamOnce 建一次连接，读到断开为止。
func (c *Client) streamOnce(ctx context.Context, out chan<- StreamEvent) error {
	req, err := c.newRequest(ctx, http.MethodGet, "stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")

	// 事件流要挂很久，不能用带总超时的那个 http.Client——它会在
	// Timeout 到点时把连接掐掉，表现为每隔固定时间断一次。
	client := &http.Client{Transport: c.http.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError("连接事件流", resp)
	}

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil // 面板正常关闭了流
			}
			return err
		}
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
			return nil
		}
	}
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
