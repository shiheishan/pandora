// mihomo 客户端：
// 配置来自 <out>/<id>.clash（Clash Meta 格式，mihomo 客户端按 UA 拿到的就是这个格式，见 render.DetectFormat / clashFlavor），
// 只取该节点那条 proxies 条目、协议参数原样使用；测试环境改动只有：
//  1. port 换成节点端实际监听的 port+20000；
//  2. server 保持 node.example.com，靠配置里的 hosts 指到 127.0.0.1（SNI、Host 默认值仍按原域名，与真实客户端一致）；
//  3. 非 REALITY 的 TLS 加 skip-cert-verify: true：mihomo 不能额外信任一张 CA，只能放行；
//     这只是测试环境改动，用户拿到的订阅不带它（除非面板本来就因 insecure 写了）；
//  4. ShadowTLS 的 plugin-opts 加 skip-cert-verify: true：节点端的握手站点被换成本地自签 TLS 站，原因同上。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

var mihomoPath string

var (
	mihomoMu    sync.Mutex
	mihomoProcs []*exec.Cmd
)

func checkMihomoBinary() (string, error) {
	bin := strings.TrimSpace(os.Getenv("MIHOMO_BIN"))
	if bin == "" {
		return "", errors.New("-client mihomo 需要环境变量 MIHOMO_BIN（官方 mihomo 可执行文件路径）")
	}
	want := strings.ToLower(strings.TrimSpace(os.Getenv("MIHOMO_SHA256")))
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("MIHOMO_SHA256 必须是 64 位十六进制的 SHA-256（可执行文件本身的哈希）")
	}
	f, err := os.Open(bin)
	if err != nil {
		return "", fmt.Errorf("打开 MIHOMO_BIN 失败: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("计算 MIHOMO_BIN 哈希失败: %w", err)
	}
	got := h.Sum(nil)
	if hex.EncodeToString(got) != want {
		return "", fmt.Errorf("MIHOMO_BIN 的 SHA-256 不符：实际 %x，期望 %s", got, want)
	}
	return bin, nil
}

// mihomoUnsupported 登记的是 mihomo 明确报出「不支持」的组合。
// 命中时结果记 MIHOMO-UNSUPPORTED <原因>，矩阵显示 SKIPPED（不算失败也不算通过）；
// 只登记 mihomo 自己日志里明确说不支持的，不要拿它掩盖连不上。
var mihomoUnsupported = []struct {
	needle, reason string
}{
	{"xhttp HTTP/3 does not support REALITY", "mihomo 不支持 REALITY + XHTTP/H3"},
}

func runMihomo(id, typ string, port int, kcfg map[string]any, kcfgErr string, env *caseEnv) string {
	clashRaw, err := os.ReadFile(filepath.Join(*outDir, id+".clash"))
	if err != nil {
		return "NO-RENDER"
	}
	var doc struct {
		Proxies []map[string]any `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(clashRaw, &doc); err != nil {
		return "CLIENT-PARSE-FAIL " + shorten(err.Error())
	}
	var proxyOut map[string]any
	for _, p := range doc.Proxies {
		if name, _ := p["name"].(string); name == id {
			proxyOut = p
			break
		}
	}
	if proxyOut == nil {
		return "SKIPPED-IN-CLASH (订阅里没有这个节点)"
	}

	srvPort, fail := startNode(id, typ, port, kcfg, kcfgErr, env)
	if fail != "" {
		return fail
	}

	proxyOut["port"] = srvPort
	if _, hasReality := proxyOut["reality-opts"]; !hasReality {
		pType, _ := proxyOut["type"].(string)
		tlsVal, _ := proxyOut["tls"].(bool)
		switch {
		case tlsVal:
			proxyOut["skip-cert-verify"] = true
		case pType == "trojan" || pType == "hysteria" || pType == "hysteria2" || pType == "tuic" || pType == "anytls":
			proxyOut["skip-cert-verify"] = true
		}
	}
	if plugin, _ := proxyOut["plugin"].(string); plugin == "shadow-tls" {
		if opts, ok := proxyOut["plugin-opts"].(map[string]any); ok {
			opts["skip-cert-verify"] = true
		}
	}

	mixedPort := freePort()
	cfg := map[string]any{
		"mixed-port":   mixedPort,
		"bind-address": "127.0.0.1",
		"allow-lan":    false,
		"mode":         "rule",
		"log-level":    "debug",
		"ipv6":         false,
		"hosts":        map[string]any{"node.example.com": "127.0.0.1"},
		"proxies":      []any{proxyOut},
		"rules":        []any{fmt.Sprintf("MATCH,%s", id)},
	}
	cfgBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return "CLIENT-PARSE-FAIL " + shorten(err.Error())
	}
	cfgPath := filepath.Join(env.tmp, id+".mihomo.yaml")
	_ = os.WriteFile(cfgPath, cfgBytes, 0o644)

	home := filepath.Join(env.tmp, "mihomo-home", id)
	must(os.MkdirAll(home, 0o755))
	logPath := filepath.Join(env.tmp, id+".mihomo.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		return "CLIENT-START-FAIL " + shorten(err.Error())
	}

	cmd := exec.Command(mihomoPath, "-d", home, "-f", cfgPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return "CLIENT-START-FAIL " + shorten(err.Error())
	}

	mihomoMu.Lock()
	mihomoProcs = append(mihomoProcs, cmd)
	mihomoMu.Unlock()

	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
		logFile.Close()
	}()
	cleanup = append(cleanup, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})

	socksAddr := fmt.Sprintf("127.0.0.1:%d", mixedPort)
	ready := false
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return mihomoFail("CLIENT-START-FAIL", logPath)
		default:
		}
		conn, err := net.DialTimeout("tcp", socksAddr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		return mihomoFail("CLIENT-START-FAIL", logPath)
	}

	res := fetchViaSOCKS(socksAddr, env.target)
	if res == "CONNECT-OK" {
		return "CONNECT-OK"
	}
	return mihomoFail(res, logPath)
}

func mihomoFail(res, logPath string) string {
	raw, _ := os.ReadFile(logPath)
	content := string(raw)
	for _, u := range mihomoUnsupported {
		if strings.Contains(content, u.needle) {
			return "MIHOMO-UNSUPPORTED " + u.reason
		}
	}
	lines := strings.Split(content, "\n")
	var candidate string
	var lastNonEmpty string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed == "" {
			continue
		}
		lastNonEmpty = trimmed
		if strings.Contains(l, "level=error") || strings.Contains(l, "level=warning") {
			candidate = trimmed
		}
	}
	if candidate == "" {
		candidate = lastNonEmpty
	}
	if candidate != "" {
		// 分开截断：res 本身可能已近 160 字，合起来截会把 mihomo 的报错整段截掉
		return shorten(res) + " | mihomo: " + shorten(candidate)
	}
	return shorten(res)
}

func killMihomoAll() {
	mihomoMu.Lock()
	for _, cmd := range mihomoProcs {
		if cmd != nil && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	mihomoMu.Unlock()
	time.Sleep(200 * time.Millisecond)
}
