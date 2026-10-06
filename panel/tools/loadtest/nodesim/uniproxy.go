// [INPUT]: 依赖 domain/nodefabric 的 ProxyUser 与事件流信封（StreamMessage、SyncUsersPayload、SyncUserDeltaPayload、SyncConfigPayload、Event* 常量），net/http
// [OUTPUT]: 对外提供 包内 uniClient（newUniClient、users、config、push、alive、status、streamLoop）、streamEvent
// [POS]: tools/loadtest/nodesim 的 UniProxy 兼容通道与 SSE，复刻 pdnd panel/client.go + stream.go：Bearer 鉴权、查询参数 node_id/node_type、ETag 只在解析成功后更新、流断开指数退避加抖动

package nodesim

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
)

const uniPrefix = "/api/v1/server/UniProxy/"

type uniClient struct {
	base   string
	nodeID string
	token  string
	// nodeType 由主循环按下发配置改写、被流 goroutine 读，和 pdnd 一样用原子值
	nodeType  atomic.Value // string
	usersETag atomic.Value // string
	// configETag 只在兼容通道的 /config 用，只有主循环碰它
	configETag string
	http       *http.Client
	stream     *http.Client
	obs        *observer
}

func newUniClient(base, nodeID, nodeType, token string, timeout time.Duration, obs *observer) *uniClient {
	// pdnd 每个节点一个显式 Transport（MaxIdleConnsPerHost 4、空闲 90 秒），
	// 事件流复用同一个 Transport、但不带总超时。
	transport := &http.Transport{MaxIdleConnsPerHost: 4, IdleConnTimeout: 90 * time.Second}
	c := &uniClient{
		base: base, nodeID: nodeID, token: token, obs: obs,
		http:   &http.Client{Timeout: timeout, Transport: transport},
		stream: &http.Client{Transport: transport},
	}
	c.nodeType.Store(nodeType)
	c.usersETag.Store("")
	return c
}

func (c *uniClient) NodeType() string { v, _ := c.nodeType.Load().(string); return v }

func (c *uniClient) setNodeType(t string) {
	if t = strings.TrimSpace(t); t != "" {
		c.nodeType.Store(t)
	}
}

// setUsersVersion 对应 pdnd SetUsersVersion：流推下来的版本与 REST ETag 同源。
func (c *uniClient) setUsersVersion(v string) {
	if v != "" {
		c.usersETag.Store(v)
	}
}

func (c *uniClient) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	q := url.Values{}
	q.Set("node_id", c.nodeID)
	q.Set("node_type", c.NodeType())
	req, err := http.NewRequestWithContext(ctx, method, c.base+uniPrefix+path+"?"+q.Encode(), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

// get 发 GET，按状态码给标签：304 记 etag_304，401 记 auth_fail，其余用调用方给的。
func (c *uniClient) get(ctx context.Context, path, ifNoneMatch, flag string) (*http.Response, time.Time, error) {
	req, err := c.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.obs.record(ctx, "node:GET "+uniPrefix+path, 0, time.Since(start), err, flag)
		return nil, start, err
	}
	return resp, start, nil
}

func (c *uniClient) finish(ctx context.Context, path string, resp *http.Response, start time.Time, flag string) {
	switch resp.StatusCode {
	case http.StatusNotModified:
		flag = flagETag304
	case http.StatusUnauthorized:
		flag = flagAuthFail
	}
	c.obs.record(ctx, "node:"+resp.Request.Method+" "+uniPrefix+path, resp.StatusCode, time.Since(start), nil, flag)
}

// users 复刻 pdnd Client.Users：带 If-None-Match，304 返回 (nil, false, nil)，
// ETag 只在整份列表解析成功之后才记下。
func (c *uniClient) users(ctx context.Context, flag string) ([]nodefabric.ProxyUser, bool, error) {
	tag, _ := c.usersETag.Load().(string)
	resp, start, err := c.get(ctx, "user", tag, flag)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		c.finish(ctx, "user", resp, start, flag)
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		c.finish(ctx, "user", resp, start, flag)
		return nil, false, httpError("拉取用户", resp)
	}
	var out struct {
		Users []nodefabric.ProxyUser `json:"users"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out)
	// 延迟记到读完响应体：几千个用户的全量列表，传输本身就是要量的那部分
	c.finish(ctx, "user", resp, start, flag)
	if err != nil {
		return nil, false, fmt.Errorf("解析用户列表: %w", err)
	}
	if tag := resp.Header.Get("ETag"); tag != "" {
		c.usersETag.Store(tag)
	}
	return out.Users, true, nil
}

// config 是兼容通道的 /config（清单里没有节点私钥时才走），同样只在解析成功后记 ETag。
func (c *uniClient) config(ctx context.Context) (map[string]any, bool, error) {
	resp, start, err := c.get(ctx, "config", c.configETag, "")
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		c.finish(ctx, "config", resp, start, "")
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		c.finish(ctx, "config", resp, start, "")
		return nil, false, httpError("拉取配置", resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	c.finish(ctx, "config", resp, start, "")
	if err != nil {
		return nil, false, err
	}
	var cfg map[string]any
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, false, fmt.Errorf("解析配置: %w", err)
	}
	c.configETag = resp.Header.Get("ETag")
	return cfg, true, nil
}

func (c *uniClient) push(ctx context.Context, traffic map[string][2]int64) error {
	if len(traffic) == 0 {
		return nil
	}
	return c.post(ctx, "push", traffic)
}

func (c *uniClient) alive(ctx context.Context, online map[string][]string) error {
	if len(online) == 0 {
		return nil
	}
	return c.post(ctx, "alive", online)
}

func (c *uniClient) status(ctx context.Context, s compatStatus) error {
	return c.post(ctx, "status", s)
}

func (c *uniClient) post(ctx context.Context, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		c.obs.record(ctx, "node:POST "+uniPrefix+path, 0, time.Since(start), err, "")
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	c.finish(ctx, path, resp, start, "")
	if resp.StatusCode != http.StatusOK {
		return httpError(path, resp)
	}
	return nil
}

func httpError(what string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s 失败：HTTP %d %s", what, resp.StatusCode, bytes.TrimSpace(snippet))
}

//------------------------------------------------------------------------------
// 事件流（SSE）
//------------------------------------------------------------------------------

// streamEvent 对应 pdnd panel.StreamEvent，用户只留模拟器用得到的字段。
type streamEvent struct {
	Type        string
	Users       []nodefabric.ProxyUser
	Version     string
	Added       []nodefabric.ProxyUser
	Removed     []int64
	FromVersion string
	ToVersion   string
}

// 退避参数与 pdnd 相同：1 秒起翻倍、30 秒封顶，再加 [0, backoff/2) 的抖动。
// 注意 pdnd 的退避在连接成功后不归位，模拟器照抄这一点。
const (
	streamMinBackoff = time.Second
	streamMaxBackoff = 30 * time.Second
)

// streamLoop 复刻 pdnd Client.Stream：自己重连，不返回错误。
func (c *uniClient) streamLoop(ctx context.Context, out chan<- streamEvent, onError func(error)) {
	backoff := streamMinBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		err := c.streamOnce(ctx, out)
		if ctx.Err() != nil {
			return
		}
		c.obs.fleet.streamDrops.Add(1)
		if err != nil && onError != nil {
			onError(err)
		}
		jitter := time.Duration(rand.Int64N(int64(backoff / 2)))
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff + jitter):
		}
		if backoff *= 2; backoff > streamMaxBackoff {
			backoff = streamMaxBackoff
		}
	}
}

// streamOnce 建一次连接读到断开。只把建连（拿到响应头）记成一次请求，
// 长连接活了多久不是延迟；事件按类型计数进 fleet。
func (c *uniClient) streamOnce(ctx context.Context, out chan<- streamEvent) error {
	req, err := c.newRequest(ctx, http.MethodGet, "stream", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	start := time.Now()
	resp, err := c.stream.Do(req)
	if err != nil {
		c.obs.record(ctx, "node:GET "+uniPrefix+"stream", 0, time.Since(start), err, "")
		return err
	}
	defer resp.Body.Close()
	c.finish(ctx, "stream", resp, start, "")
	if resp.StatusCode != http.StatusOK {
		return httpError("连接事件流", resp)
	}
	c.obs.fleet.streamOpened()
	defer c.obs.fleet.streamsOpen.Add(-1)

	reader := bufio.NewReaderSize(resp.Body, 64<<10)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		payload, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		ev, ok := parseStreamEvent([]byte(payload))
		if !ok {
			c.obs.fleet.countEvent("unparsed")
			continue
		}
		c.obs.fleet.countEvent(ev.Type)
		select {
		case out <- ev:
		case <-ctx.Done():
			return nil
		}
	}
}

// parseStreamEvent 用面板侧的信封与载荷类型解码；不认识的事件跳过不断开。
func parseStreamEvent(raw []byte) (streamEvent, bool) {
	var msg nodefabric.StreamMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return streamEvent{}, false
	}
	e := streamEvent{Type: msg.Event}
	switch msg.Event {
	case nodefabric.EventSyncUsers:
		var p nodefabric.SyncUsersPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return streamEvent{}, false
		}
		e.Users, e.Version = p.Users, p.Version
	case nodefabric.EventSyncUserDelta:
		var p nodefabric.SyncUserDeltaPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return streamEvent{}, false
		}
		e.Added, e.Removed = p.Delta.Added, p.Delta.Removed
		e.FromVersion, e.ToVersion = p.FromVersion, p.ToVersion
	case nodefabric.EventSyncConfig:
		var p nodefabric.SyncConfigPayload
		if err := json.Unmarshal(msg.Data, &p); err != nil {
			return streamEvent{}, false
		}
	default:
		return streamEvent{}, false
	}
	return e, true
}
