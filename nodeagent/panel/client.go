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
	"time"

	"github.com/aegispanel/nodeagent/core"
)

// Client 对应面板上的一个节点。
//
// 一个进程可以带多个节点（不同协议、不同端口），每个节点一个 Client ——
// 它们的凭据、配置、用户表都是独立的。
type Client struct {
	http     *http.Client
	base     string
	nodeID   string
	nodeType string
	token    string

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
	return &Client{
		// 显式给 Transport：默认的 http.DefaultTransport 全局共享，
		// 一个节点的连接池被别的节点拖垮时排查起来毫无头绪。
		http: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		base:     o.BaseURL,
		nodeID:   o.NodeID,
		nodeType: o.NodeType,
		token:    o.Token,
	}
}

func (c *Client) NodeID() string   { return c.nodeID }
func (c *Client) NodeType() string { return c.nodeType }

func (c *Client) endpoint(path string) string {
	q := url.Values{}
	q.Set("node_id", c.nodeID)
	q.Set("node_type", c.nodeType)
	q.Set("token", c.token)
	return c.base + "/api/v1/server/UniProxy/" + path + "?" + q.Encode()
}

// Config 拉取节点配置。changed 为 false 表示配置与上次一致（面板回了 304）。
func (c *Client) Config(ctx context.Context) (cfg map[string]any, changed bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("config"), nil)
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

type usersResponse struct {
	Users []struct {
		ID          int64  `json:"id"`
		UUID        string `json:"uuid"`
		SpeedLimit  int    `json:"speed_limit"`
		DeviceLimit int    `json:"device_limit"`
	} `json:"users"`
}

// Users 拉取该节点应放行的用户全量列表。
func (c *Client) Users(ctx context.Context) ([]core.User, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("user"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError("拉取用户", resp)
	}

	var out usersResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析用户列表: %w", err)
	}
	users := make([]core.User, 0, len(out.Users))
	for _, u := range out.Users {
		users = append(users, core.User{
			ID: u.ID, UUID: u.UUID,
			SpeedLimit: u.SpeedLimit, DeviceLimit: u.DeviceLimit,
		})
	}
	return users, nil
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

// Status 上报宿主机资源占用。
//
// 这个调用在很长一段时间里根本不存在，后果比听上去严重：面板的
// nodes.last_heartbeat_at 只由这个接口写入，所以跑着我们自己 agent 的
// 节点在面板看来「从未上报过心跳」。而订阅下发要求节点至少上报过一次
// 心跳，于是这些节点被静默排除在订阅之外 —— agent 在正常同步用户、
// 正常转发流量，面板却认为它不存在，两边都不报错。
//
// 上报是按节点发的，同一台机器上有几个节点就发几次相同的资源数字。
// 看着重复，但面板那边一次调用同时刷新 nodes 和 servers 两张表的心跳，
// 少发一个节点，那个节点就会被判定成离线。
func (c *Client) Status(ctx context.Context, s core.SystemStatus) error {
	return c.post(ctx, "status", s)
}

func (c *Client) post(ctx context.Context, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(path), bytes.NewReader(body))
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
