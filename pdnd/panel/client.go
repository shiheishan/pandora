// Package panel 是节点端与面板之间的通信层。
//
// 走的是 UniProxy 协议（Xboard / V2board 兼容），而不是自定义协议：
// 这样同一个面板既能带我们自研的节点端，也能带现成的 XrayR / V2bX，
// 用户换节点端不用换面板，换面板也不用换节点端。
package panel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// Client 对应面板上的一个节点。
//
// 一个进程可以带多个节点（不同协议、不同端口），每个节点一个 Client ——
// 它们的凭据、配置、用户表都是独立的。
type Client struct {
	http   *http.Client
	base   string
	nodeID string
	// nodeType 是当前生效的协议。初值来自本地配置文件，之后以面板下发的
	// 为准——管理员在面板上改了协议，节点端要能自己跟上，而不是等人来
	// 改 config.json 再重启。
	//
	// 用原子值而不是普通字段：主循环在一个 goroutine 里读写它，但
	// endpoint() 会被同一轮里的多次请求调用，而 Go 的内存模型不保证
	// 无同步的读写可见性——竞态检测器会直接报出来。
	nodeType atomic.Value // string
	// usersETag 是上一次成功解析的用户列表版本，用来换 304。
	usersETag atomic.Value // string
	token     string

	// 上一次配置的 ETag。面板据此回 304，
	// 省掉一次全量解析和可能的内核重建。
	etag string
}

type Options struct {
	BaseURL  string
	NodeID   string
	NodeType string
	Token    string
	Timeout  time.Duration
}

func New(o Options) *Client {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	c := &Client{
		// 显式给 Transport：默认的 http.DefaultTransport 全局共享，
		// 一个节点的连接池被别的节点拖垮时排查起来毫无头绪。
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		base:   o.BaseURL,
		nodeID: o.NodeID,
		token:  o.Token,
	}
	// atomic.Value 没法在结构体字面量里给初值，构造完再塞。
	c.nodeType.Store(o.NodeType)
	return c
}

func (c *Client) NodeID() string { return c.nodeID }
func (c *Client) NodeType() string {
	v, _ := c.nodeType.Load().(string)
	return v
}

// SetNodeType 记下面板下发的协议，之后的请求都用它。
//
// 面板的认证以库里的协议为准，不会因为 URL 上写的是旧值就拒绝；但保持
// 一致能让面板那边少记一条「协议不一致」的日志，也让抓包排查时看到的
// 东西和实际相符。
func (c *Client) SetNodeType(t string) {
	if t = strings.TrimSpace(t); t != "" {
		c.nodeType.Store(t)
	}
}

func (c *Client) endpoint(path string) string {
	q := url.Values{}
	q.Set("node_id", c.nodeID)
	q.Set("node_type", c.NodeType())
	return c.base + "/api/v1/server/UniProxy/" + path + "?" + q.Encode()
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil {
		return nil, err
	}
	// Query credentials leak into reverse-proxy, CDN and access logs. Pandora's
	// UniProxy gateway prefers bearer authentication while retaining query-token
	// fallback only for third-party compatibility clients.
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

// Config 拉取节点配置。changed 为 false 表示配置与上次一致（面板回了 304）。
func (c *Client) Config(ctx context.Context) (cfg map[string]any, changed bool, err error) {
	req, err := c.newRequest(ctx, http.MethodGet, "config", nil)
	if err != nil {
		return nil, false, err
	}
	if c.etag != "" {
		req.Header.Set("If-None-Match", c.etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, false, nil
	case http.StatusOK:
	default:
		return nil, false, httpError("拉取配置", resp)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false, err
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, false, fmt.Errorf("解析配置: %w", err)
	}
	// ETag 只在解析成功后才更新 —— 否则一次坏响应会让后续请求
	// 一直拿到 304，节点永远停在错误的配置上
	c.etag = resp.Header.Get("ETag")
	return cfg, true, nil
}

// wireUser 是面板下发的用户，REST 和事件流两条路共用。
type wireUser struct {
	ID          int64  `json:"id"`
	UUID        string `json:"uuid"`
	SpeedLimit  int    `json:"speed_limit"`
	DeviceLimit int    `json:"device_limit"`
}

type usersResponse struct {
	Users []wireUser `json:"users"`
}

// Users 拉取该节点应放行的用户全量列表。
// Users 拉取该节点应当放行的用户。
//
// 第二个返回值表示列表是否变过。没变时返回 (nil, false, nil)——调用方
// 不该把那个 nil 当成"用户被清空了"，那会把所有人踢下线。
//
// 带 If-None-Match：用户列表绝大多数轮次不变，让面板回 304 能省掉两头的
// 序列化和解析。几十个用户看不出差别，几千个用户每 15 秒一次全量往返
// 就很可观了。
func (c *Client) Users(ctx context.Context) ([]core.User, bool, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "user", nil)
	if err != nil {
		return nil, false, err
	}
	if tag, _ := c.usersETag.Load().(string); tag != "" {
		req.Header.Set("If-None-Match", tag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return nil, false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, httpError("拉取用户", resp)
	}

	var out usersResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("解析用户列表: %w", err)
	}
	users := make([]core.User, 0, len(out.Users))
	for _, u := range out.Users {
		users = append(users, core.User{
			ID: u.ID, UUID: u.UUID,
			SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit,
		})
	}
	// 解析成功之后才记 ETag。解析失败时记下来的话，下一轮会拿着这个
	// ETag 换回 304，于是那份没解析成的列表就永远同步不上了。
	if tag := resp.Header.Get("ETag"); tag != "" {
		c.usersETag.Store(tag)
	}
	return users, true, nil
}

// Push 上报流量增量。格式为 {"<uid>": [upload, download]}。
func (c *Client) Push(ctx context.Context, traffic []core.UserTraffic) error {
	if len(traffic) == 0 {
		return nil
	}
	payload := make(map[string][2]int64, len(traffic))
	for _, t := range traffic {
		// 同一用户在多个入站上都有流量时要累加而不是覆盖：
		// 一个节点同时开 VLESS 和 Hysteria2 是常见配置，
		// 覆盖会让其中一份流量凭空消失
		key := strconv.FormatInt(t.ID, 10)
		cur := payload[key]
		payload[key] = [2]int64{cur[0] + t.Upload, cur[1] + t.Download}
	}
	return c.post(ctx, "push", payload)
}

// Alive 上报各用户的在线来源 IP，供设备数限制使用。
func (c *Client) Alive(ctx context.Context, online map[int64][]string) error {
	if len(online) == 0 {
		return nil
	}
	payload := make(map[string][]string, len(online))
	for id, ips := range online {
		payload[strconv.FormatInt(id, 10)] = ips
	}
	return c.post(ctx, "alive", payload)
}

func (c *Client) post(ctx context.Context, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 必须读完再关，否则连接无法复用，高频上报会不断新建 TCP
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return httpError(path, resp)
	}
	return nil
}

func httpError(what string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("%s 失败：HTTP %d %s", what, resp.StatusCode, bytes.TrimSpace(snippet))
}

// SetUsersVersion 记下当前的用户列表版本。
//
// 事件流推下来全量或增量之后调用，让下一轮轮询带着这个版本去换 304，
// 不用把刚推下来的东西再拉一遍。版本和 REST 的 ETag 是同一个值——面板
// 两条路用的是同一个计算函数。
func (c *Client) SetUsersVersion(v string) {
	if v != "" {
		c.usersETag.Store(v)
	}
}
