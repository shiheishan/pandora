// Command aegis-agent 是运行在节点上的代理。
//
// 设计原则（对应 AGT-001..010）：
//   - 只出不进：Agent 主动连控制面，节点不监听任何管理端口
//   - 私钥不出机器：本地生成 Ed25519 密钥对，只上报公钥
//   - 配置必验签：签名或有效期不对就拒绝应用，继续跑上一份（AGT-007/008）
//   - 离线自治：控制面不可达时保持最后一份好配置，不自行降级（AGT-010）
//
// 用法：
//
//	aegis-agent bootstrap --server http://host:9003 --token <一次性令牌> --name <节点名>
//	aegis-agent run       --server http://host:9003
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"time"
)

const agentVersion = "0.1.0"

type state struct {
	Server          string `json:"server"`
	NodeID          string `json:"node_id"`
	Serial          int    `json:"serial"`
	PrivateKey      string `json:"private_key"` // base64，仅本机可读
	ConfigPublicKey string `json:"config_public_key"`
	ConfigKeyID     string `json:"config_key_id"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "用法: aegis-agent <bootstrap|run> [flags]")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "bootstrap":
		err = cmdBootstrap()
	case "run":
		err = cmdRun()
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

//------------------------------------------------------------------------------

func statePath() string {
	if p := os.Getenv("AEGIS_AGENT_STATE"); p != "" {
		return p
	}
	return "/var/lib/aegis-agent/state.json"
}

func loadState() (*state, error) {
	b, err := os.ReadFile(statePath())
	if err != nil {
		return nil, fmt.Errorf("读取状态失败（是否已 bootstrap？）: %w", err)
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func saveState(s *state) error {
	p := statePath()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// 0600：状态文件含私钥，同机其他用户不得读取
	return os.WriteFile(p, b, 0o600)
}

//------------------------------------------------------------------------------

func cmdBootstrap() error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	server := fs.String("server", "", "Node 网关地址，如 http://1.2.3.4:9003")
	token := fs.String("token", "", "管理员签发的一次性引导令牌")
	name := fs.String("name", "", "节点名称")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}
	if *server == "" || *token == "" || *name == "" {
		return errors.New("--server --token --name 均必填")
	}

	// 密钥在本机生成，私钥永不离开这台机器（NODE-009）
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}

	host, _ := os.Hostname()
	body, _ := json.Marshal(map[string]any{
		"token":         *token,
		"node_name":     *name,
		"public_key":    base64.StdEncoding.EncodeToString(pub),
		"agent_version": agentVersion,
		"hostname":      host,
		"cpu_cores":     runtime.NumCPU(),
		"memory_mb":     memoryMB(),
		"disk_gb":       diskGB("/"),
	})

	// 服务端已经下线单阶段引导：POST /v1/nodes/bootstrap 不看内容一律 426，
	// 要求走 /v1/nodes/enrollments 的两阶段接入（begin → 装好本地产物并给出
	// 证据摘要 → commit）。aegis-agent 没有实现那套流程，它的角色由
	// pandora-native 承担。与其发一个注定被拒的请求、让人对着 HTTP 426 猜，
	// 不如在这里说清楚。
	if true {
		return fmt.Errorf("aegis-agent bootstrap 已停用：服务端只接受两阶段接入。\n" +
			"请改用 pandora-native：\n" +
			"  pandora-native enrollment begin --server <控制面> --token <令牌> --name <节点名>\n" +
			"  pandora-native enrollment commit --binary-sha256 … --config-sha256 … --unit-sha256 … --preflight-sha256 …")
	}

	resp, err := http.Post(*server+"/v1/nodes/bootstrap",
		"application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("连接控制面失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("引导被拒绝 (HTTP %d): %s", resp.StatusCode, string(raw))
	}

	var out struct {
		NodeID          string `json:"node_id"`
		Serial          int    `json:"serial"`
		SpiffeID        string `json:"spiffe_id"`
		ConfigPublicKey string `json:"config_public_key"`
		ConfigKeyID     string `json:"config_key_id"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}

	if err := saveState(&state{
		Server:          *server,
		NodeID:          out.NodeID,
		Serial:          out.Serial,
		PrivateKey:      base64.StdEncoding.EncodeToString(priv),
		ConfigPublicKey: out.ConfigPublicKey,
		ConfigKeyID:     out.ConfigKeyID,
	}); err != nil {
		return err
	}

	fmt.Printf("引导成功\n  节点 ID : %s\n  身份序号: %d\n  SPIFFE  : %s\n  状态文件: %s\n",
		out.NodeID, out.Serial, out.SpiffeID, statePath())
	return nil
}

//------------------------------------------------------------------------------

type agent struct {
	st     *state
	priv   ed25519.PrivateKey
	cfgPub ed25519.PublicKey
	client *http.Client

	// 当前生效的配置。控制面不可达时继续用它，绝不自行清空（AGT-010）
	curVersion int
	curHash    string
}

func cmdRun() error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	server := fs.String("server", "", "覆盖状态文件里的控制面地址")
	once := fs.Bool("once", false, "只跑一轮后退出，用于集成测试")
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	st, err := loadState()
	if err != nil {
		return err
	}
	if *server != "" {
		st.Server = *server
	}

	privRaw, err := base64.StdEncoding.DecodeString(st.PrivateKey)
	if err != nil || len(privRaw) != ed25519.PrivateKeySize {
		return errors.New("状态文件里的私钥损坏")
	}
	cfgPub, err := base64.StdEncoding.DecodeString(st.ConfigPublicKey)
	if err != nil {
		return errors.New("状态文件里的配置公钥损坏")
	}

	a := &agent{
		st: st, priv: privRaw, cfgPub: cfgPub,
		client: &http.Client{Timeout: 15 * time.Second},
	}

	fmt.Printf("Agent 启动 node=%s serial=%d server=%s\n", st.NodeID, st.Serial, st.Server)

	if *once {
		return a.tick()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, terminationSignals()...)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()

	for {
		if err := a.tick(); err != nil {
			// 控制面异常不退出、不改配置：这正是 AGT-010 要的行为
			fmt.Fprintln(os.Stderr, "本轮失败（保持现有配置继续运行）:", err)
		}
		select {
		case <-t.C:
		case <-stop:
			fmt.Println("收到停止信号，退出")
			return nil
		}
	}
}

// tick 是一个完整的心跳周期：上报状态 → 若有新配置则拉取、验签、应用、回报。
func (a *agent) tick() error {
	hb, err := a.heartbeat()
	if err != nil {
		return err
	}
	if hb.DesiredConfigVersion == 0 || hb.DesiredConfigVersion == a.curVersion {
		return nil
	}
	fmt.Printf("检测到新配置 v%d（当前 v%d），开始拉取\n", hb.DesiredConfigVersion, a.curVersion)
	return a.applyConfig()
}

type hbResp struct {
	NodeStatus           string `json:"node_status"`
	DesiredConfigVersion int    `json:"desired_config_version"`
	IntervalSeconds      int    `json:"interval_seconds"`
}

func (a *agent) heartbeat() (*hbResp, error) {
	m := collectMetrics()
	payload := map[string]any{
		"agent_version":          agentVersion,
		"applied_config_version": a.curVersion,
		"applied_config_hash":    a.curHash,
		"cpu_cores":              runtime.NumCPU(),
		"memory_mb":              m.MemTotalMB,
		"disk_gb":                m.DiskTotalGB,
		"runtime_status":         "running",
	}
	// CPU 使用率需要两次采样才有意义，首轮拿不到就不上报这一项 ——
	// 报一个假的 0% 比不报更糟，会在图表上留下一段虚假的空闲期
	if m.CPUBasisPoints >= 0 {
		payload["metrics"] = m
	} else {
		mm := m
		mm.CPUBasisPoints = 0
		payload["metrics"] = mm
		payload["metrics_partial"] = true
	}
	body, _ := json.Marshal(payload)
	raw, err := a.do("POST", "/v1/nodes/heartbeat", body)
	if err != nil {
		return nil, err
	}
	var out hbResp
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *agent) applyConfig() error {
	raw, err := a.do("GET", "/v1/nodes/config", nil)
	if err != nil {
		return err
	}
	var cfg struct {
		Version   int             `json:"version"`
		Payload   json.RawMessage `json:"payload"`
		Hash      string          `json:"hash"`
		Signature string          `json:"signature"`
		KeyID     string          `json:"key_id"`
		ExpiresAt time.Time       `json:"expires_at"`
		Sources   []string        `json:"sources"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}

	// 1) 内容哈希必须与正文对得上，否则正文在传输中被换过
	sum := sha256.Sum256(cfg.Payload)
	if base64.StdEncoding.EncodeToString(sum[:]) != cfg.Hash {
		a.report(cfg.Version, "failed", "内容哈希与正文不符")
		return errors.New("配置哈希校验失败，已拒绝应用")
	}

	// 2) 签名与有效期。任一不过就保留旧配置（AGT-007/008）
	if !verifySig(a.cfgPub, cfg.Hash, cfg.Signature, cfg.ExpiresAt) {
		a.report(cfg.Version, "failed", "签名无效或已过期")
		return errors.New("配置签名校验失败，已拒绝应用并保留上一版本")
	}
	a.report(cfg.Version, "verified", "签名与哈希校验通过")

	// 3) 预检：确认是合法 JSON 对象再落盘，避免写进去一个跑不起来的配置
	var probe map[string]any
	if err := json.Unmarshal(cfg.Payload, &probe); err != nil {
		a.report(cfg.Version, "precheck_failed", "配置不是 JSON 对象")
		return err
	}

	// 4) 原子切换：先写临时文件再 rename，中途断电不会留下半个文件
	dir := filepath.Dir(statePath())
	tmp := filepath.Join(dir, ".runtime.json.tmp")
	final := filepath.Join(dir, "runtime.json")
	if err := os.WriteFile(tmp, cfg.Payload, 0o600); err != nil {
		a.report(cfg.Version, "failed", "写入临时文件失败")
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		a.report(cfg.Version, "failed", "原子切换失败")
		return err
	}

	a.curVersion, a.curHash = cfg.Version, cfg.Hash
	a.report(cfg.Version, "switched", fmt.Sprintf("已应用，来源 %v", cfg.Sources))
	fmt.Printf("配置 v%d 已应用（来源 %v，%d 个键）\n", cfg.Version, cfg.Sources, len(probe))
	return nil
}

func verifySig(pub ed25519.PublicKey, hashB64, sigB64 string, exp time.Time) bool {
	sum, err := base64.StdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		return false
	}
	if time.Now().After(exp) {
		return false
	}
	return ed25519.Verify(pub, append(sum, []byte(exp.UTC().Format(time.RFC3339))...), sig)
}

func (a *agent) report(version int, phase, detail string) {
	body, _ := json.Marshal(map[string]any{
		"version": version, "phase": phase, "detail": detail,
	})
	// 回报失败不影响本地已完成的动作，仅记录
	if _, err := a.do("POST", "/v1/nodes/config/report", body); err != nil {
		fmt.Fprintln(os.Stderr, "回报配置状态失败:", err)
	}
}

// do 发起带签名的请求。签名规则必须与服务端 CanonicalPayload 完全一致。
func (a *agent) do(method, path string, body []byte) ([]byte, error) {
	ts := time.Now().UTC().Format(time.RFC3339)
	sum := sha256.Sum256(body)
	payload := []byte(method + "\n" + path + "\n" + a.st.NodeID + "\n" + ts + "\n" +
		base64.StdEncoding.EncodeToString(sum[:]))
	sig := ed25519.Sign(a.priv, payload)

	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, a.st.Server+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Node-Id", a.st.NodeID)
	req.Header.Set("X-Node-Ts", ts)
	req.Header.Set("X-Node-Sig", base64.StdEncoding.EncodeToString(sig))

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s → HTTP %d: %s", method, path, resp.StatusCode, string(raw))
	}
	return raw, nil
}

//------------------------------------------------------------------------------
// 资产采集：只读 /proc 与 statfs，不引入任何第三方依赖

func memoryMB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var kb int
	if _, err := fmt.Sscanf(string(b), "MemTotal: %d kB", &kb); err != nil {
		return 0
	}
	return kb / 1024
}

func diskGB(path string) int {
	total, _ := diskUsage(path)
	return total
}
