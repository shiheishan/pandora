package kernel

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aegispanel/nodeagent/internal/confnum"
)

// XHTTPMode is the wire scheduling mode. The values intentionally follow the
// published XHTTP names while the session implementation remains Pandora
// owned.
type XHTTPMode string

const (
	XHTTPAuto       XHTTPMode = "auto"
	XHTTPPacketUp   XHTTPMode = "packet-up"
	XHTTPStreamUp   XHTTPMode = "stream-up"
	XHTTPStreamOne  XHTTPMode = "stream-one"
	XHTTPStreamDown XHTTPMode = "stream-down"
)

type XHTTPRange struct {
	From int
	To   int
}

// XHTTPConfig is a typed, immutable-at-runtime representation of the XHTTP
// surface. It contains no xray protobuf or transport object.
type XHTTPConfig struct {
	Host               string
	Path               string
	Mode               XHTTPMode
	Headers            map[string]string
	MaxPost            XHTTPRange
	MinIntervalMS      XHTTPRange
	MaxBufferedPosts   int
	StreamUpServerSecs XHTTPRange

	SessionPlacement     string
	SessionKey           string
	SeqPlacement         string
	SeqKey               string
	UplinkHTTPMethod     string
	UplinkDataPlacement  string
	UplinkDataKey        string
	UplinkChunkSize      XHTTPRange
	ServerMaxHeaderBytes int
}

func ParseXHTTPConfig(raw map[string]any) (XHTTPConfig, error) {
	c := XHTTPConfig{
		Mode:               XHTTPAuto,
		Headers:            make(map[string]string),
		MaxPost:            XHTTPRange{From: 1_000_000, To: 1_000_000},
		MinIntervalMS:      XHTTPRange{From: 30, To: 30},
		MaxBufferedPosts:   30,
		StreamUpServerSecs: XHTTPRange{From: 20, To: 80},
		SessionPlacement:   "path", SessionKey: "session",
		SeqPlacement: "path", SeqKey: "seq",
		UplinkHTTPMethod: "POST", UplinkDataPlacement: "body", UplinkDataKey: "data",
		ServerMaxHeaderBytes: 8192,
	}
	if v := rawString(raw, "host"); v != "" {
		c.Host = v
	}
	path, err := normalizeXHTTPPath(rawString(raw, "path"))
	if err != nil {
		return XHTTPConfig{}, err
	}
	c.Path = path
	if mode := strings.ToLower(strings.TrimSpace(rawString(raw, "mode"))); mode != "" {
		c.Mode = XHTTPMode(mode)
	}
	if !validXHTTPMode(c.Mode) {
		return XHTTPConfig{}, fmt.Errorf("xhttp mode %q 不受支持", c.Mode)
	}
	if headers, err := parseXHTTPHeaders(rawValue(raw, "headers")); err != nil {
		return XHTTPConfig{}, err
	} else {
		for key, value := range headers {
			c.Headers[key] = value
		}
	}
	if c.MaxPost, err = parseXHTTPRange(rawValue(raw, "sc_max_each_post_bytes"), c.MaxPost, 1, 64<<20); err != nil {
		return XHTTPConfig{}, fmt.Errorf("xhttp sc_max_each_post_bytes: %w", err)
	}
	if c.MinIntervalMS, err = parseXHTTPRange(rawValue(raw, "sc_min_posts_interval_ms"), c.MinIntervalMS, 0, 24*60*60*1000); err != nil {
		return XHTTPConfig{}, fmt.Errorf("xhttp sc_min_posts_interval_ms: %w", err)
	}
	if c.StreamUpServerSecs, err = parseXHTTPRange(rawValue(raw, "sc_stream_up_server_secs"), c.StreamUpServerSecs, 0, 24*60*60); err != nil {
		return XHTTPConfig{}, fmt.Errorf("xhttp sc_stream_up_server_secs: %w", err)
	}
	if v, ok := rawInt(rawValue(raw, "sc_max_buffered_posts")); ok {
		if v < 0 || v > 10000 {
			return XHTTPConfig{}, fmt.Errorf("xhttp sc_max_buffered_posts 超出范围")
		}
		c.MaxBufferedPosts = v
	}
	for key, dst := range map[string]*string{
		"session_placement": &c.SessionPlacement, "session_key": &c.SessionKey,
		"seq_placement": &c.SeqPlacement, "seq_key": &c.SeqKey,
		"uplink_data_placement": &c.UplinkDataPlacement, "uplink_data_key": &c.UplinkDataKey,
	} {
		if value := rawString(raw, key); value != "" {
			*dst = value
		}
	}
	for _, placement := range []struct{ name, value string }{
		{"session_placement", c.SessionPlacement}, {"seq_placement", c.SeqPlacement}, {"uplink_data_placement", c.UplinkDataPlacement},
	} {
		if !validXHTTPPlacement(placement.value, placement.name == "uplink_data_placement") {
			return XHTTPConfig{}, fmt.Errorf("xhttp %s=%q 无效", placement.name, placement.value)
		}
	}
	if method := rawString(raw, "uplink_http_method"); method != "" {
		c.UplinkHTTPMethod = strings.ToUpper(method)
	}
	if c.UplinkHTTPMethod != http.MethodPost && c.UplinkHTTPMethod != http.MethodGet {
		return XHTTPConfig{}, fmt.Errorf("xhttp uplink_http_method 仅支持 GET 或 POST")
	}
	if c.UplinkHTTPMethod == http.MethodGet && c.UplinkDataPlacement == "body" {
		// GET 没有请求体：上行数据只能放请求头或 Cookie（Xray 同样拒绝这种组合）。
		return XHTTPConfig{}, fmt.Errorf("xhttp uplink_http_method=GET 需要 uplink_data_placement 为 header 或 cookie")
	}
	if c.UplinkChunkSize, err = parseXHTTPRange(rawValue(raw, "uplink_chunk_size"), XHTTPRange{From: c.MaxPost.From, To: c.MaxPost.To}, 64, 64<<20); err != nil {
		return XHTTPConfig{}, fmt.Errorf("xhttp uplink_chunk_size: %w", err)
	}
	if v, ok := rawInt(rawValue(raw, "server_max_header_bytes")); ok {
		if v < 1024 || v > 1<<20 {
			return XHTTPConfig{}, fmt.Errorf("xhttp server_max_header_bytes 超出范围")
		}
		c.ServerMaxHeaderBytes = v
	}
	return c, nil
}

func (c XHTTPConfig) RequestPath(sessionID, seq string) (string, error) {
	path := c.Path
	if c.SessionPlacement == "path" {
		if strings.TrimSpace(sessionID) == "" {
			return "", fmt.Errorf("xhttp path session_id 不能为空")
		}
		path = appendXHTTPPath(path, sessionID)
	}
	if c.SeqPlacement == "path" {
		if strings.TrimSpace(seq) == "" {
			return "", fmt.Errorf("xhttp path seq 不能为空")
		}
		path = appendXHTTPPath(path, seq)
	}
	return path, nil
}

func (c XHTTPConfig) ApplyRequestMeta(req *http.Request, sessionID, seq string) error {
	if req == nil || req.URL == nil {
		return fmt.Errorf("xhttp request 不能为空")
	}
	path, err := c.RequestPath(sessionID, seq)
	if err != nil {
		return err
	}
	pathPart, queryPart, _ := strings.Cut(path, "?")
	req.URL.Path = pathPart
	if queryPart != "" {
		req.URL.RawQuery = queryPart
	}
	applyXHTTPMeta(req, c.SessionPlacement, c.SessionKey, sessionID)
	applyXHTTPMeta(req, c.SeqPlacement, c.SeqKey, seq)
	for key, value := range c.Headers {
		req.Header.Set(key, value)
	}
	if c.Host != "" {
		req.Host = c.Host
	}
	return nil
}

func (c XHTTPConfig) ExtractRequestMeta(req *http.Request) (sessionID, seq string, err error) {
	return c.extractRequestMeta(req, false)
}

func (c XHTTPConfig) extractRequestMeta(req *http.Request, allowMissingSeq bool) (sessionID, seq string, err error) {
	return c.extractRequestMetaOptions(req, false, allowMissingSeq)
}

// extractRequestMetaOptions handles packet metadata and stream-one clients.
// Stream-one deliberately uses the base path and carries no path metadata.
func (c XHTTPConfig) extractRequestMetaOptions(req *http.Request, allowMissingSession, allowMissingSeq bool) (sessionID, seq string, err error) {
	if req == nil || req.URL == nil {
		return "", "", fmt.Errorf("xhttp request 不能为空")
	}
	path, _, _ := strings.Cut(c.Path, "?")
	path = strings.TrimSuffix(path, "/")
	requestPath := req.URL.Path
	if path != "" && requestPath != path && !strings.HasPrefix(requestPath, path+"/") {
		return "", "", fmt.Errorf("xhttp path 不匹配")
	}
	rest := strings.TrimPrefix(requestPath, path)
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	// Xray's stream-one mode starts on the base path and carries no session or
	// sequence metadata. Accept that one request shape before applying the
	// regular packet/session metadata rules.
	if allowMissingSession && allowMissingSeq && c.SessionPlacement == "path" && (len(parts) == 0 || parts[0] == "") {
		return "", "", nil
	}
	missingSeq := false
	if c.SessionPlacement == "path" {
		if len(parts) == 0 || parts[0] == "" {
			return "", "", fmt.Errorf("xhttp 缺少 session_id")
		}
		sessionID, _ = url.PathUnescape(parts[0])
	}
	if c.SeqPlacement == "path" {
		index := 0
		if c.SessionPlacement == "path" {
			index = 1
		}
		if len(parts) <= index || parts[index] == "" {
			if allowMissingSeq {
				missingSeq = true
			} else {
				return "", "", fmt.Errorf("xhttp 缺少 seq")
			}
		} else {
			seq, _ = url.PathUnescape(parts[index])
		}
	}
	if c.SessionPlacement != "path" {
		sessionID = readXHTTPMeta(req, c.SessionPlacement, c.SessionKey)
	}
	if c.SeqPlacement != "path" {
		seq = readXHTTPMeta(req, c.SeqPlacement, c.SeqKey)
	}
	if sessionID == "" && allowMissingSession {
		// stream-one 不带任何会话元数据（会话放在 query / header / cookie 时同理）。
		return "", "", nil
	}
	if sessionID == "" || (!allowMissingSeq && seq == "") {
		return "", "", fmt.Errorf("xhttp 会话元数据不完整")
	}
	if allowMissingSeq && (missingSeq || seq == "") {
		return sessionID, "", nil
	}
	if _, err := strconv.ParseUint(seq, 10, 64); err != nil {
		return "", "", fmt.Errorf("xhttp seq 无效")
	}
	return sessionID, seq, nil
}

var (
	errXHTTPMethodNotAllowed = errors.New("xhttp method not allowed")
	errXHTTPPayloadTooLarge  = errors.New("xhttp uplink packet too large")
)

// uplinkPayload 按 uplink_data_placement 取 packet-up 上行包的负载（照 Xray 的
// splithttp 服务端）：body 读请求体；header 拼接 {key}-0、{key}-1… 请求头；cookie
// 拼接 {key}_0、{key}_1… Cookie；auto 三者依次拼接。请求头与 Cookie 里是
// base64url（无填充）。超过 sc_max_each_post_bytes 即拒。
func (c XHTTPConfig) uplinkPayload(req *http.Request) ([]byte, error) {
	limit := int64(c.MaxPost.To)
	var out []byte
	placement := c.UplinkDataPlacement
	if placement == "header" || placement == "auto" {
		var encoded strings.Builder
		for i := 0; ; i++ {
			chunk := req.Header.Get(c.UplinkDataKey + "-" + strconv.Itoa(i))
			if chunk == "" {
				break
			}
			encoded.WriteString(chunk)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded.String())
		if err != nil {
			return nil, fmt.Errorf("xhttp uplink header payload: %w", err)
		}
		out = append(out, decoded...)
	}
	if placement == "cookie" || placement == "auto" {
		var encoded strings.Builder
		for i := 0; ; i++ {
			cookie, err := req.Cookie(c.UplinkDataKey + "_" + strconv.Itoa(i))
			if err != nil {
				break
			}
			encoded.WriteString(cookie.Value)
		}
		decoded, err := base64.RawURLEncoding.DecodeString(encoded.String())
		if err != nil {
			return nil, fmt.Errorf("xhttp uplink cookie payload: %w", err)
		}
		out = append(out, decoded...)
	}
	if placement != "header" && placement != "cookie" && req.Body != nil {
		body := io.Reader(req.Body)
		if limit > 0 {
			body = io.LimitReader(req.Body, limit+1)
		}
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, err
		}
		out = append(out, data...)
	}
	if limit > 0 && int64(len(out)) > limit {
		return nil, errXHTTPPayloadTooLarge
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

// classifyRequest 判定一次请求在会话里的角色。
//
//   - stream-one：任何形状都按双工处理（含 Pandora 探针用的「会话/序号」路径）。
//   - packet-up / stream-down：GET 是下行，上行方法须带会话与序号。
//   - auto / stream-up：照 Xray 服务端的逐请求判定。auto 由客户端自己选模式——
//     Xray 在 REALITY 上选 stream-one（配了 downloadSettings 时选 stream-up），
//     其余（TLS h2/h3、明文 h1）选 packet-up；mihomo 同样。所以 auto 必须三种
//     都收：不带会话的上行请求是 stream-one；带会话的 GET 是下行；带会话与序号
//     的上行请求是 packet-up；带会话不带序号的上行请求是 stream-up。stream-up
//     模式不收 packet-up（与 Xray 一致）。
func (c XHTTPConfig) classifyRequest(req *http.Request) (XHTTPRequestKind, string, string, error) {
	uplinkMethod := req.Method == c.UplinkHTTPMethod
	switch c.Mode {
	case XHTTPStreamOne:
		if !uplinkMethod {
			return 0, "", "", errXHTTPMethodNotAllowed
		}
		sessionID, seq, err := c.extractRequestMetaOptions(req, true, true)
		return XHTTPRequestDuplex, sessionID, seq, err
	case XHTTPPacketUp, XHTTPStreamDown:
		get := req.Method == http.MethodGet
		if !uplinkMethod && !get {
			return 0, "", "", errXHTTPMethodNotAllowed
		}
		sessionID, seq, err := c.extractRequestMetaOptions(req, false, get)
		// 上行方法配成 GET 时，带序号的 GET 是上行包，不带序号的才是下行（审查 X6）。
		if get && (!uplinkMethod || seq == "") {
			return XHTTPRequestDownlink, sessionID, seq, err
		}
		return XHTTPRequestPacket, sessionID, seq, err
	}
	get := req.Method == http.MethodGet
	if !uplinkMethod && !get {
		return 0, "", "", errXHTTPMethodNotAllowed
	}
	sessionID, seq, err := c.extractRequestMetaOptions(req, true, true)
	if err != nil {
		return 0, "", "", err
	}
	if sessionID == "" {
		if !uplinkMethod || seq != "" {
			return 0, "", "", fmt.Errorf("xhttp stream-one 请求不应带序号")
		}
		return XHTTPRequestDuplex, "", "", nil
	}
	// 上行方法配成 GET 时，带序号的 GET 才是上行包（Xray 同此判定）。
	if get && (seq == "" || !uplinkMethod) {
		return XHTTPRequestDownlink, sessionID, "", nil
	}
	if seq == "" {
		return XHTTPRequestStreamUp, sessionID, "", nil
	}
	if c.Mode == XHTTPStreamUp {
		return 0, "", "", fmt.Errorf("xhttp stream-up 模式不接受 packet-up 上行")
	}
	return XHTTPRequestPacket, sessionID, seq, nil
}

// xhttpUsesSessions 表示这个 mode 下会出现跨请求的会话（需要会话中转）。
// 只有 stream-one 是一请求一连接。
func xhttpUsesSessions(mode XHTTPMode) bool {
	return mode != XHTTPStreamOne
}

func validXHTTPMode(mode XHTTPMode) bool {
	switch mode {
	case XHTTPAuto, XHTTPPacketUp, XHTTPStreamUp, XHTTPStreamOne, XHTTPStreamDown:
		return true
	}
	return false
}

func validXHTTPPlacement(value string, allowBody bool) bool {
	if allowBody && (value == "body" || value == "auto") {
		return true
	}
	switch value {
	case "path", "query", "header", "cookie":
		return true
	}
	return false
}

func normalizeXHTTPPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	parts := strings.SplitN(path, "?", 2)
	base := parts[0]
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	if strings.ContainsAny(base, "\r\n") {
		return "", fmt.Errorf("xhttp path 包含控制字符")
	}
	if len(parts) == 1 {
		return base, nil
	}
	return base + "?" + parts[1], nil
}

func appendXHTTPPath(path, value string) string {
	base, query, _ := strings.Cut(path, "?")
	base = strings.TrimSuffix(base, "/") + "/" + url.PathEscape(value) + "/"
	if query != "" {
		return base + "?" + query
	}
	return base
}

func applyXHTTPMeta(req *http.Request, placement, key, value string) {
	switch placement {
	case "query":
		q := req.URL.Query()
		q.Set(key, value)
		req.URL.RawQuery = q.Encode()
	case "header":
		req.Header.Set(key, value)
	case "cookie":
		req.AddCookie(&http.Cookie{Name: key, Value: value})
	}
}

func readXHTTPMeta(req *http.Request, placement, key string) string {
	switch placement {
	case "query":
		return req.URL.Query().Get(key)
	case "header":
		return req.Header.Get(key)
	case "cookie":
		if cookie, err := req.Cookie(key); err == nil {
			return cookie.Value
		}
	}
	return ""
}

func rawValue(raw map[string]any, key string) any {
	if value, ok := raw[key]; ok {
		return value
	}
	camel := strings.ReplaceAll(key, "_", "")
	for candidate, value := range raw {
		if strings.EqualFold(strings.ReplaceAll(candidate, "_", ""), camel) {
			return value
		}
	}
	return nil
}

func rawString(raw map[string]any, key string) string {
	value, _ := rawValue(raw, key).(string)
	return strings.TrimSpace(value)
}

// rawInt 读一个整数字段，数值形态的归一见 confnum。
func rawInt(value any) (int, bool) {
	return confnum.Int(value)
}

func parseXHTTPRange(value any, fallback XHTTPRange, minValue, maxValue int) (XHTTPRange, error) {
	if value == nil {
		return fallback, nil
	}
	var from, to int
	switch v := value.(type) {
	case map[string]any:
		var ok bool
		from, ok = rawInt(v["from"])
		if !ok {
			return XHTTPRange{}, fmt.Errorf("from 必须是整数")
		}
		to, ok = rawInt(v["to"])
		if !ok {
			return XHTTPRange{}, fmt.Errorf("to 必须是整数")
		}
	default:
		return XHTTPRange{}, fmt.Errorf("必须是 {from,to}")
	}
	if from < minValue || to < from || to > maxValue {
		return XHTTPRange{}, fmt.Errorf("范围无效")
	}
	return XHTTPRange{From: from, To: to}, nil
}

func parseXHTTPHeaders(value any) (map[string]string, error) {
	out := make(map[string]string)
	if value == nil {
		return out, nil
	}
	switch values := value.(type) {
	case map[string]string:
		for key, item := range values {
			out[key] = item
		}
	case map[string]any:
		for key, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("xhttp header %q 必须是字符串", key)
			}
			out[key] = text
		}
	default:
		return nil, fmt.Errorf("xhttp headers 必须是对象")
	}
	for key, value := range out {
		if strings.TrimSpace(key) == "" || strings.ContainsAny(key+value, "\r\n") || http.CanonicalHeaderKey(key) == "" {
			return nil, fmt.Errorf("xhttp header %q 无效", key)
		}
	}
	return out, nil
}
