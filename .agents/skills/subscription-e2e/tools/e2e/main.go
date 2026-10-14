// 端到端：pdnd NativeCore 按「面板下发给节点的配置文档」（node/service 的 buildNodeConfigDoc 真实输出）起入站，
// 客户端按「面板渲染给用户的订阅」里的出站去连（支持 sing-box 与 mihomo 两种客户端，经 -client 选择），
// 经 SOCKS 拉一个本地 HTTP 目标，拿到约定的响应体才算通。
//
// 两种客户端共用同一套节点端与环境准备。测试环境改动清单如下，不碰协议参数：
//  1. 端口统一 +20000，监听 127.0.0.1；
//  2. node.example.com 解析到 127.0.0.1（sing-box 经 hosts DNS，mihomo 经 hosts 配置；SNI / Host 仍是 node.example.com）；
//  3. 非 REALITY 的 TLS 对本次自签证书放行（模拟运营方给 server_host 配了有效证书）：
//     sing-box 是信任这张证书，mihomo 不能加 CA，用 skip-cert-verify: true；
//     REALITY 的 dest、ShadowTLS 的握手站点都换成本地一个 TLS 站点（证书 www.example.com）。
//
// 第 3 条意味着：这里通了，不代表真实环境里自签证书、缺 SNI 的节点也能通。
//
// 用法：e2e -client singbox|mihomo -out <渲染输出目录> -work <临时目录> [-only id1,id2] [-log debug]
// mihomo 需要环境变量 MIHOMO_BIN 与 MIHOMO_SHA256。
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
	"golang.org/x/net/proxy"
)

// 与 panel 渲染测试的 fixtureUUID 一致（虚构值）
const userUUID = "3f1c2b4a-5d6e-4f70-8a9b-0c1d2e3f4a5b"

const (
	portShift   = 20000
	caseTimeout = 25 * time.Second
)

var (
	client   = flag.String("client", "singbox", "客户端：singbox 或 mihomo")
	outDir   = flag.String("out", "", "渲染输出目录：含 node_config.json、fixtures_index.json 和 <id>.singbox")
	workDir  = flag.String("work", "", "临时目录：放自签证书和每个用例的客户端配置")
	only     = flag.String("only", "", "只跑这些夹具，逗号分隔")
	logLevel = flag.String("log", "panic", "sing-box 客户端日志级别（排查时用 debug；mihomo 固定 debug 写到 <id>.mihomo.log）")
)

// 每个用例结束后要关掉的东西（NativeCore、客户端实例等）
var cleanup []func()

// decoyCertPEM 是本地「借用握手站点」的自签证书。生产里这是真实站点的有效证书；
// ShadowTLS v3 客户端会校验它，所以让外层出站信任这张（只改信任，不改参数）。
var decoyCertPEM string

// caseEnv 是所有用例共用的测试环境（一次进程只建一次）
type caseEnv struct {
	certPath, keyPath string // 节点端 TLS 证书（node.example.com 自签）
	certPEM           string // 同一张证书的 PEM，sing-box 客户端信任它
	dest              string // 本地 TLS 诱饵站 host:port：REALITY dest、ShadowTLS 握手站点
	target            string // 本地 HTTP 目标 host:port，响应体 PANDORA-OK
	tmp               string // <work>/e2e：放证书、每个用例的客户端配置与日志
}

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

func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func startNode(id, typ string, port int, kcfg map[string]any, kcfgErr string, env *caseEnv) (srvPort int, fail string) {
	if kcfg == nil {
		// 订阅给了这个节点，节点端却拿不到配置：用户必然连不上，算失败
		return 0, "NODE-CONFIG-REJECTED " + shorten(kcfgErr)
	}

	srvPort = port + portShift
	rawCfg := map[string]any{}
	for k, v := range kcfg {
		rawCfg[k] = v
	}
	rawCfg["server_port"] = srvPort
	if _, ok := rawCfg["cert_path"]; ok {
		rawCfg["cert_path"], rawCfg["key_path"] = env.certPath, env.keyPath
	}
	if _, ok := rawCfg["dest"]; ok {
		rawCfg["dest"] = env.dest
	}
	if typ == "shadowtls" {
		// 握手目标换成本地 TLS 站点，不出网；只换地址
		if _, ok := rawCfg["server"]; !ok {
			return 0, "E2E-PRECONDITION server(host:port) 未由面板翻译写出"
		}
		rawCfg["server"] = env.dest
	}
	nc := kernel.NewNativeCore(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cleanup = append(cleanup, cancel)
	must(nc.Start(ctx))
	cleanup = append(cleanup, func() { nc.Close() })
	tag := "in-" + id
	if err := nc.ApplyInbound(&core.InboundConfig{Tag: tag, Protocol: typ, Listen: "127.0.0.1", Port: srvPort, Raw: rawCfg}, nil); err != nil {
		return 0, "SERVER-START-FAIL " + err.Error()
	}
	if err := nc.AddUsers(tag, []core.User{{ID: 1, UUID: userUUID}}); err != nil {
		return 0, "SERVER-ADDUSER-FAIL " + err.Error()
	}
	return srvPort, ""
}

func fetchViaSOCKS(socksAddr, target string) string {
	d, err := proxy.SOCKS5("tcp", socksAddr, nil, &net.Dialer{Timeout: 5 * time.Second})
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

func runCase(id, typ string, port int, kcfg map[string]any, kcfgErr string, env *caseEnv) (res string) {
	defer func() {
		if r := recover(); r != nil {
			res = fmt.Sprintf("PANIC %v", r)
		}
	}()
	switch *client {
	case "singbox":
		return runSingbox(id, typ, port, kcfg, kcfgErr, env)
	case "mihomo":
		return runMihomo(id, typ, port, kcfg, kcfgErr, env)
	default:
		return "UNKNOWN-CLIENT " + *client
	}
}

func main() {
	flag.Parse()
	if *outDir == "" || *workDir == "" || (*client != "singbox" && *client != "mihomo") {
		fmt.Fprintln(os.Stderr, "用法: e2e -client singbox|mihomo -out <渲染输出目录> -work <临时目录> [-only id1,id2] [-log debug]")
		os.Exit(2)
	}

	if *client == "mihomo" {
		bin, err := checkMihomoBinary()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		mihomoPath = bin
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

	env := &caseEnv{
		certPath: certPath,
		keyPath:  keyPath,
		certPEM:  string(certPEM),
		dest:     destLn.Addr().String(),
		target:   target.Addr().String(),
		tmp:      tmp,
	}

	// 节点端配置取自真实的 buildNodeConfigDoc 输出（node/service 的 overlay 测试生成）
	var nodeCfg map[string]map[string]any
	raw, err := os.ReadFile(filepath.Join(*outDir, "node_config.json"))
	must(err)
	must(json.Unmarshal(raw, &nodeCfg))
	// buildNodeConfigDoc 拒绝的夹具及原因（可能没有这个文件）
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
			ch <- runCase(id, f.Type, f.Port, nodeCfg[id], nodeCfgErrs[id], env)
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

	killMihomoAll()
}

func shorten(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > 160 {
		return s[:160]
	}
	return s
}
