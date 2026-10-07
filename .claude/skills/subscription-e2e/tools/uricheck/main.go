// uricheck：URI（base64 分享链接列表）订阅的静态检查。本机没有 v2rayN / Xray 客户端，
// 只核对：整体能 base64 解码、每行能解析、scheme 认得、主机与端口齐全，
// 再按 v2rayN 分享链接的约定核对传输参数与 REALITY 参数——这些缺了客户端不报错，只是连不上。
//
// 用法：uricheck a.uri b.uri ...
// 输出：每个文件一行「文件名  URI-OK (links=N)」，有问题时每条一行「URI-WARN 说明」，
// 解码失败为「URI-FAIL 原因」。
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

func main() {
	for _, p := range os.Args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Printf("%-36s READ-FAIL %v\n", p, err)
			continue
		}
		body := strings.TrimSpace(string(b))
		dec, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			fmt.Printf("%-36s URI-FAIL 整体不是标准 base64: %v\n", p, err)
			continue
		}
		var links []string
		for _, l := range strings.Split(string(dec), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				links = append(links, l)
			}
		}
		var issues []string
		for _, l := range links {
			if msg := checkLink(l); msg != "" {
				issues = append(issues, msg)
			}
		}
		if len(issues) == 0 {
			fmt.Printf("%-36s URI-OK (links=%d)\n", p, len(links))
			continue
		}
		for _, i := range issues {
			fmt.Printf("%-36s URI-WARN %s\n", p, i)
		}
	}
}

func checkLink(l string) string {
	if strings.HasPrefix(l, "vmess://") {
		return checkVMess(strings.TrimPrefix(l, "vmess://"))
	}
	u, err := url.Parse(l)
	if err != nil {
		return "解析失败: " + err.Error()
	}
	name := u.Fragment
	if name == "" {
		name = u.Scheme + " 链接"
	}
	switch u.Scheme {
	case "vless", "trojan", "ss", "hysteria2", "tuic", "anytls", "naive+https", "juicity", "socks5", "http", "https":
	default:
		return fmt.Sprintf("%s: 不认识的 scheme %q", name, u.Scheme)
	}
	if u.Hostname() == "" {
		return name + ": 缺主机"
	}
	if port, err := strconv.Atoi(u.Port()); err != nil || port <= 0 || port > 65535 {
		return name + ": 端口缺失或非法"
	}
	if u.User == nil || u.User.Username() == "" {
		return name + ": 缺凭据（userinfo）"
	}
	q := u.Query()
	switch u.Scheme {
	case "ss":
		// SIP002：userinfo 是 base64url(method:password)
		raw, err := base64.RawURLEncoding.DecodeString(u.User.Username())
		if err != nil || !strings.Contains(string(raw), ":") {
			return name + ": ss 的 userinfo 不是 base64url(method:password)"
		}
	case "vless", "trojan":
		if msg := checkStream(name, q.Get("type"), q.Get("security"), q.Get("path"), q.Get("serviceName"), q); msg != "" {
			return msg
		}
		if u.Scheme == "vless" && q.Get("encryption") == "" {
			return name + ": vless 缺 encryption=none"
		}
	case "hysteria2":
		if q.Get("obfs") != "" && q.Get("obfs-password") == "" {
			return name + ": 有 obfs 却没有 obfs-password"
		}
	}
	return ""
}

func checkStream(name, network, security, path, serviceName string, q url.Values) string {
	switch security {
	case "reality":
		if q.Get("pbk") == "" {
			return name + ": REALITY 缺 pbk（公钥）"
		}
		if q.Get("sni") == "" {
			return name + ": REALITY 缺 sni"
		}
	case "tls", "none", "":
	default:
		return fmt.Sprintf("%s: 不认识的 security=%q", name, security)
	}
	switch network {
	case "ws", "httpupgrade", "xhttp":
		if path == "" {
			return fmt.Sprintf("%s: type=%s 缺 path", name, network)
		}
	case "grpc":
		if serviceName == "" {
			return name + ": type=grpc 缺 serviceName"
		}
	case "mkcp":
		return name + ": 传输名写成了 mkcp，v2rayN 系只认 kcp"
	}
	return ""
}

func checkVMess(payload string) string {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return "vmess: 载荷不是标准 base64"
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "vmess: 载荷不是 JSON"
	}
	name, _ := m["ps"].(string)
	for _, k := range []string{"add", "port", "id", "net"} {
		if s, _ := m[k].(string); s == "" {
			return fmt.Sprintf("vmess %s: 缺 %s", name, k)
		}
	}
	network, _ := m["net"].(string)
	path, _ := m["path"].(string)
	switch network {
	case "ws", "httpupgrade", "xhttp":
		if path == "" {
			return fmt.Sprintf("vmess %s: net=%s 缺 path", name, network)
		}
	case "grpc":
		// v2rayN 的 vmess 链接把 gRPC 服务名放在 path
		if path == "" {
			return fmt.Sprintf("vmess %s: net=grpc 缺服务名（放在 path）", name)
		}
	}
	return ""
}
