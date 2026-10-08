//go:build unix

package nodesim

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 1000 节点的资源自检：一台 2c4g 压测机要能同时扛 1000 个模拟节点，nodesim 自己吃多少 CPU 与内存，
// 在这里量出来。
//
// 做法：假面板（TLS + HTTP/2、事件流保活 20 秒、1 万用户、拉取 15 秒 / 上报 60 秒的真实节拍）跑在
// 子进程里——测试二进制自己重新执行 TestScaleFakePanelProcess——所以父进程的 CPU 与内存只属于
// nodesim 一边，不混进假面板的开销。
//
// 默认跑 30 秒起跑错开 + 60 秒稳态；LOADTEST_SCALE_STEADY 可改稳态时长（秒）。-short 与竞态检测下跳过。

const (
	scaleNodes = 1000
	scaleUsers = 10000
	// 起跑错开窗口：1000 个节点各拉两份 1 万人的全量名单（REST 一份、事件流一份），
	// 压测机上真跑时建议 180 秒（见 README），测试里压到 30 秒以免拖慢 CI
	scaleStagger = 30 * time.Second

	scaleChildEnv = "LT_SCALE_PANEL_DIR"
)

// 资源上限只是「出了大问题」的哨兵，不是及格线：实测远低于它们（README 与报告里有数），
// 留出 CI 共享机的波动余量。
const (
	scaleMaxSteadyCores = 1.0
	scaleMaxRSSMiB      = 1536
)

// TestScaleFakePanelProcess 只在被 TestScale1000Nodes 当子进程拉起时有用：起一个 TLS 假面板，
// 把清单、信任根与地址写进目录，然后等父进程关掉标准输入。直接 go test 时跳过。
func TestScaleFakePanelProcess(t *testing.T) {
	dir := os.Getenv(scaleChildEnv)
	if dir == "" {
		t.Skip("child process of TestScale1000Nodes only")
	}
	g := newFakeGatewayWith(t, scaleNodes, scaleUsers, 15, 60, fakeOpts{tls: true, keepalive: 20 * time.Second})
	m := g.manifest()
	if err := m.Save(filepath.Join(dir, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: g.srv.Certificate().Raw})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), ca, 0o600); err != nil {
		t.Fatal(err)
	}
	// 地址最后写：父进程以它出现为就绪信号
	if err := os.WriteFile(filepath.Join(dir, "url"), []byte(g.srv.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type scaleChild struct {
	cmd *exec.Cmd
	in  io.WriteCloser
}

func startFakePanelProcess(t *testing.T, dir string) *scaleChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestScaleFakePanelProcess$", "-test.v")
	cmd.Env = append(os.Environ(), scaleChildEnv+"="+dir)
	log, err := os.Create(filepath.Join(dir, "panel.log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = log, log
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &scaleChild{cmd: cmd, in: in}
	t.Cleanup(func() {
		_ = in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = log.Close()
	})
	return c
}

func waitForFile(t *testing.T, path string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return b
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (see panel.log next to it)", path)
	return nil
}

// cpuSeconds 是本进程累计的用户 + 系统 CPU 秒数。
func cpuSeconds(t *testing.T) float64 {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	tv := func(v syscall.Timeval) float64 { return float64(v.Sec) + float64(v.Usec)/1e6 }
	return tv(ru.Utime) + tv(ru.Stime)
}

// peakRSSMiB 是本进程的峰值常驻内存（getrusage 的单位：Linux 千字节，macOS 字节）。
func peakRSSMiB(t *testing.T) float64 {
	t.Helper()
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "darwin" {
		return float64(ru.Maxrss) / (1 << 20)
	}
	return float64(ru.Maxrss) / 1024
}

func TestScale1000Nodes(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("1000-node resource check is skipped under -short and the race detector")
	}
	// 客户端 + 假面板各约 2000 个套接字；Go 会把软上限抬到硬上限，仍不够就跳过而不是误报
	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil && lim.Cur < 2*scaleNodes+500 {
		t.Skipf("RLIMIT_NOFILE %d is too low for %d nodes", lim.Cur, scaleNodes)
	}
	steady := 60 * time.Second
	if v, err := strconv.Atoi(os.Getenv("LOADTEST_SCALE_STEADY")); err == nil && v >= 20 {
		steady = time.Duration(v) * time.Second // 起跑错开之后还要 15 秒落定，稳态至少留 20 秒才量得到
	}

	dir := t.TempDir()
	startFakePanelProcess(t, dir)
	url := string(waitForFile(t, filepath.Join(dir, "url"), 90*time.Second))
	caPEM := waitForFile(t, filepath.Join(dir, "ca.pem"), time.Second)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("fake panel CA did not parse")
	}
	m, err := ltkit.LoadManifest(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Nodes) != scaleNodes || len(m.Users) != scaleUsers {
		t.Fatalf("manifest has %d nodes / %d users", len(m.Nodes), len(m.Users))
	}

	opt := DefaultOptions()
	opt.NodeURL = url
	opt.TLSConfig = &tls.Config{RootCAs: pool}
	opt.Stagger = scaleStagger
	opt.Duration = scaleStagger + steady
	opt.Progress = 0
	opt.Seed = 1000
	var nodeLog bytes.Buffer
	opt.Stderr = &nodeLog // 节点侧错误样本（前 20 条），失败时打出来
	rec := ltkit.NewRecorder("nodes", 10*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), opt.Duration)
	defer cancel()
	type result struct {
		s   fleetSummary
		err error
	}
	done := make(chan result, 1)
	cpu0 := cpuSeconds(t)
	start := time.Now()
	go func() {
		s, err := Run(ctx, m, opt, rec)
		done <- result{s, err}
	}()

	// 起跑错开结束再留 15 秒让名单同步落定，之后才算稳态
	settle := scaleStagger + 15*time.Second
	time.Sleep(settle)
	cpuSteady0, steadyAt := cpuSeconds(t), time.Now()
	var midMem runtime.MemStats
	runtime.ReadMemStats(&midMem)
	goroutines := runtime.NumGoroutine()
	// 回收之后的存活堆：HeapInuse 含还没回收的垃圾（1 万人的名单一份就几 MB，起跑时解了两千份），
	// 存活堆才是模拟节点自己要常驻的内存
	runtime.GC()
	var liveMem runtime.MemStats
	runtime.ReadMemStats(&liveMem)
	if p := os.Getenv("LOADTEST_SCALE_HEAP_PROFILE"); p != "" {
		if f, err := os.Create(p); err == nil {
			_ = pprof.Lookup("heap").WriteTo(f, 0)
			_ = f.Close()
		}
	}

	res := <-done
	if res.err != nil {
		t.Fatal(res.err)
	}
	cpuEnd, end := cpuSeconds(t), time.Now()
	startupCPU := cpuSteady0 - cpu0
	steadyCores := (cpuEnd - cpuSteady0) / end.Sub(steadyAt).Seconds()
	peakRSS := peakRSSMiB(t)
	rep := rec.Snapshot()
	s := res.s

	line := fmt.Sprintf("nodes=%d users=%d stagger=%s steady=%s | CPU: startup %.1f core-s over %s, steady %.3f cores | peak RSS %.0f MiB, live heap %.0f MiB (after GC; before %.0f MiB in use), goroutines %d | req=%d qps=%.0f p99=%.1fms 5xx=%d streams_peak=%d %s/%s",
		scaleNodes, scaleUsers, scaleStagger, steady, startupCPU, settle.Round(time.Second), steadyCores, peakRSS,
		float64(liveMem.HeapAlloc)/(1<<20), float64(midMem.HeapInuse)/(1<<20), goroutines, rep.Totals.Count, rep.Totals.QPS, rep.Totals.P99MS, rep.Totals.Server5x,
		s.StreamsPeak, runtime.GOOS, runtime.GOARCH)
	t.Log(line)
	_ = start
	if sum := os.Getenv("GITHUB_STEP_SUMMARY"); sum != "" {
		if f, err := os.OpenFile(sum, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "\n### nodesim 1000-node resource check (ncpu=%d)\n\n%s\n", runtime.NumCPU(), line)
			_ = f.Close()
		}
	}

	if problems := strictProblems(rep, s); len(problems) > 0 {
		t.Fatalf("strict problems: %v", problems)
	}
	if s.Started != scaleNodes || s.Launched != scaleNodes {
		t.Fatalf("launched %d, started %d, want %d each", s.Launched, s.Started, scaleNodes)
	}
	if s.StreamsPeak < scaleNodes*99/100 {
		t.Fatalf("only %d of %d event streams were ever open at once", s.StreamsPeak, scaleNodes)
	}
	if nodeLog.Len() > 0 {
		t.Logf("node-side error samples:\n%s", nodeLog.String())
	}
	// 收尾那一刻的在途请求可能被掐断，记一两条不算；成批出错就是真问题
	if s.NodeErrors > scaleNodes/100 {
		t.Fatalf("%d node-side errors", s.NodeErrors)
	}
	if steadyCores > scaleMaxSteadyCores {
		t.Fatalf("steady CPU %.2f cores exceeds the %.1f-core sentinel", steadyCores, scaleMaxSteadyCores)
	}
	if peakRSS > scaleMaxRSSMiB {
		t.Fatalf("peak RSS %.0f MiB exceeds the %d MiB sentinel", peakRSS, scaleMaxRSSMiB)
	}
}
