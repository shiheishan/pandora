// 端到端：pdnd NativeCore 按「面板下发给节点的配置」（BuildNodeConfig 的真实输出）起入站，
// sing-box 客户端按「面板渲染给用户的 sing-box 订阅」里的出站去连，
// 经 SOCKS 拉一个本地 HTTP 目标，拿到约定的响应体才算通。
//
// 只对测试环境做三处必要改动，不碰协议参数：
//  1. 端口统一 +20000，监听 127.0.0.1；
//  2. node.example.com 经 sing-box 的 hosts DNS 解析到 127.0.0.1（SNI / Host 仍是 node.example.com）；
//  3. 非 REALITY 的 TLS 出站信任本次自签证书（模拟运营方给 server_host 配了有效证书）；
//     REALITY 的 dest、ShadowTLS 的握手站点都换成本地一个 TLS 站点（证书 www.example.com）。
//
// 第 3 条意味着：这里通了，不代表真实环境里自签证书、缺 SNI 的节点也能通。
//
// 用法：e2e -out <渲染输出目录> -work <临时目录> [-only id1,id2] [-log debug]
// 输出：每个夹具一行「id  结果」，结果是 CONNECT-OK / CONNECT-FAIL … / SKIPPED-IN-SINGBOX … 等。
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/aegispanel/nodeagent/kernel"
	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	sjson "github.com/sagernet/sing/common/json"
	"golang.org/x/net/proxy"
)

// 与 panel 渲染测试的 fixtureUUID 一致（虚构值）
const userUUID = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

const (
	portShift   = 20000
	caseTimeout = 25 * time.Second
)

var (
	outDir   = flag.String("out", "", "渲染输出目录：含 node_config.json、fixtures_index.json 和 <id>.singbox")
	workDir  = flag.String("work", "", "临时目录：放自签证书和每个用例的客户端配置")
	only     = flag.String("only", "", "只跑这些夹具，逗号分隔")
	logLevel = flag.String("log", "panic", "sing-box 客户端日志级别（排查时用 debug）")
)

// 每个用例结束后要关掉的东西（NativeCore、sing-box 实例）
var cleanup []func()

// decoyCertPEM 是本地「借用握手站点」的自签证书。生产里这是真实站点的有效证书；
// ShadowTLS v3 客户端会校验它，所以让外层出站信任这张（只改信任，不改参数）。
var decoyCertPEM string

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func selfSigned(names ...string) (certPEM, keyPEM []byte) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: names[0]},
		DNSNames:              names,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	must(err)
	kd, err := x509.MarshalECPrivateKey(key)
	must(err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
}

// quietServe 起一个 HTTP 服务；探测连接断开产生的 TLS 握手错误不打到输出里，免得混进结果表
func quietServe(ln net.Listener, body string) {
	srv := &http.Server{
		Handler:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }),
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go srv.Serve(ln)
}

func main() {
	flag.Parse()
	if *outDir == "" || *workDir == "" {
		fmt.Fprintln(os.Stderr, "用法: e2e -out <渲染输出目录> -work <临时目录> [-only id1,id2] [-log debug]")
		os.Exit(2)
	}
	tmp := filepath.Join(*workDir, "e2e")
	must(os.MkdirAll(tmp, 0o755))
	certPEM, keyPEM := selfSigned("node.example.com", "sni.example.com")
	certPath, keyPath := filepath.Join(tmp, "c.pem"), filepath.Join(tmp, "k.pem")
	must(os.WriteFile(certPath, certPEM, 0o644))
	must(os.WriteFile(keyPath, keyPEM, 0o600))

	// 目标 HTTP：经代理拉到 PANDORA-OK 才算通
	target, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	quietServe(target, "PANDORA-OK")

	// REALITY 的 dest 与 ShadowTLS 的握手站点：本地 TLS 站点
	dc, dk := selfSigned("www.example.com")
	decoyCertPEM = string(dc)
	dcert, err := tls.X509KeyPair(dc, dk)
	must(err)
	destLn, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{dcert}, NextProtos: []string{"h2", "http/1.1"}})
	must(err)
	quietServe(destLn, "decoy")

	// 节点端配置取自真实的 BuildNodeConfig 输出（nodefabric 的 overlay 测试生成）
	var nodeCfg map[string]map[string]any
	raw, err := os.ReadFile(filepath.Join(*outDir, "node_config.json"))
	must(err)
	must(json.Unmarshal(raw, &nodeCfg))
	// BuildNodeConfig 拒绝下发的夹具及原因（可能没有这个文件）
	nodeCfgErrs := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(*outDir, "node_config_errors.json")); err == nil {
		must(json.Unmarshal(raw, &nodeCfgErrs))
	}
	var index map[string]struct {
		Type string `json:"type"`
		Port int    `json:"port"`
	}
	raw, err = os.ReadFile(filepath.Join(*outDir, "fixtures_index.json"))
	must(err)
	must(json.Unmarshal(raw, &index))
	ids := make([]string, 0, len(index))
	for id := range index {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	for _, id := range ids {
		if *only != "" && !strings.Contains(","+*only+",", ","+id+",") {
			fmt.Printf("%-32s %s\n", id, "NOT-SELECTED")
			continue
		}
		f := index[id]
		ch := make(chan string, 1)
		cleanup = nil
		go func() {
			ch <- runCase(id, f.Type, f.Port, nodeCfg[id], nodeCfgErrs[id], certPath, keyPath, string(certPEM), destLn.Addr().String(), target.Addr().String(), tmp)
		}()
		var res string
		select {
		case res = <-ch:
		case <-time.After(caseTimeout):
			res = "HARNESS-TIMEOUT"
		}
		for _, c := range cleanup {
			go c()
		}
		fmt.Printf("%-32s %s\n", id, res)
	}
}

func runCase(id, typ string, port int, kcfg map[string]any, kcfgErr, certPath, keyPath, certPEM, dest, target, tmp string) (res string) {
	defer func() {
		if r := recover(); r != nil {
			res = fmt.Sprintf("PANIC %v", r)
		}
	}()
	sbRaw, err := os.ReadFile(filepath.Join(*outDir, id+".singbox"))
	if err != nil {
		return "NO-RENDER"
	}
	var sb map[string]any
	must(json.Unmarshal(sbRaw, &sb))
	var proxyOut map[string]any
	var extraOuts []any
	outs, _ := sb["outbounds"].([]any)
	for _, o := range outs {
		m := o.(map[string]any)
		switch m["type"] {
		case "selector", "urltest", "direct", "block", "dns":
			continue
		}
		if m["tag"] == id {
			proxyOut = m
		} else {
			// 成对出站（如 shadowtls 外层）一起带上
			extraOuts = append(extraOuts, m)
		}
	}
	if proxyOut == nil {
		return "SKIPPED-IN-SINGBOX (订阅里没有这个节点)"
	}
	if kcfg == nil {
		// 订阅给了这个节点，节点端却拿不到配置：用户必然连不上，算失败
		return "NODE-CONFIG-REJECTED " + shorten(kcfgErr)
	}

	// ---- 节点端 ----
	srvPort := port + portShift
	rawCfg := map[string]any{}
	for k, v := range kcfg {
		rawCfg[k] = v
	}
	rawCfg["server_port"] = srvPort
	if _, ok := rawCfg["cert_path"]; ok {
		rawCfg["cert_path"], rawCfg["key_path"] = certPath, keyPath
	}
	if _, ok := rawCfg["dest"]; ok {
		rawCfg["dest"] = dest
	}
	if typ == "shadowtls" {
		// 握手目标换成本地 TLS 站点，不出网；只换地址
		if _, ok := rawCfg["server"]; !ok {
			return "E2E-PRECONDITION server(host:port) 未由面板翻译写出"
		}
		rawCfg["server"] = dest
	}
	nc := kernel.NewNativeCore(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cleanup = append(cleanup, cancel)
	must(nc.Start(ctx))
	cleanup = append(cleanup, func() { nc.Close() })
	tag := "in-" + id
	if err := nc.ApplyInbound(&core.InboundConfig{Tag: tag, Protocol: typ, Listen: "127.0.0.1", Port: srvPort, Raw: rawCfg}, nil); err != nil {
		return "SERVER-START-FAIL " + err.Error()
	}
	if err := nc.AddUsers(tag, []core.User{{ID: 1, UUID: userUUID}}); err != nil {
		return "SERVER-ADDUSER-FAIL " + err.Error()
	}

	// ---- 客户端（sing-box，出站原样取自订阅，只改端口与信任） ----
	proxyOut["server_port"] = srvPort
	if t, ok := proxyOut["tls"].(map[string]any); ok && t["enabled"] == true {
		if _, reality := t["reality"]; !reality {
			t["certificate"] = []string{certPEM}
		}
	}
	for _, e := range extraOuts {
		m := e.(map[string]any)
		if m["server_port"] != nil {
			m["server_port"] = srvPort
		}
		if t, ok := m["tls"].(map[string]any); ok && m["type"] == "shadowtls" {
			t["certificate"] = []string{decoyCertPEM}
		}
	}
	tagName := proxyOut["tag"].(string)
	mixedLn, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	mixedPort := mixedLn.Addr().(*net.TCPAddr).Port
	mixedLn.Close()
	client := map[string]any{
		"log": map[string]any{"level": *logLevel},
		"dns": map[string]any{"servers": []any{map[string]any{"type": "hosts", "tag": "hosts",
			"predefined": map[string]any{"node.example.com": "127.0.0.1"}}}},
		"inbounds":  []any{map[string]any{"type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": mixedPort}},
		"outbounds": append(append([]any{proxyOut}, extraOuts...), map[string]any{"type": "direct", "tag": "direct"}),
		"route":     map[string]any{"final": tagName, "default_domain_resolver": "hosts"},
	}
	cb, _ := json.Marshal(client)
	// 留一份客户端配置，排查时可以直接拿去手跑
	_ = os.WriteFile(filepath.Join(tmp, id+".client.json"), cb, 0o644)
	sctx := include.Context(context.Background())
	opts, err := sjson.UnmarshalExtendedContext[option.Options](sctx, cb)
	if err != nil {
		return "CLIENT-PARSE-FAIL " + err.Error()
	}
	b, err := box.New(box.Options{Context: sctx, Options: opts})
	if err != nil {
		return "CLIENT-NEW-FAIL " + err.Error()
	}
	cleanup = append(cleanup, func() { b.Close() })
	if err := b.Start(); err != nil {
		return "CLIENT-START-FAIL " + err.Error()
	}

	// ---- 拨号 ----
	d, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", mixedPort), nil, &net.Dialer{Timeout: 5 * time.Second})
	must(err)
	hc := &http.Client{Timeout: 6 * time.Second, Transport: &http.Transport{Dial: d.Dial}}
	resp, err := hc.Get("http://" + target + "/")
	if err != nil {
		return "CONNECT-FAIL " + shorten(err.Error())
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) == "PANDORA-OK" {
		return "CONNECT-OK"
	}
	return "CONNECT-WRONG-BODY " + shorten(string(body))
}

func shorten(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160]
	}
	return s
}
