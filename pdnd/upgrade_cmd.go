package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/aegispanel/nodeagent/node"
	"github.com/aegispanel/nodeagent/panel"
	"github.com/aegispanel/nodeagent/upgrade"
)

// buildSerial 由发布构建注入（build-release.sh 的 -X main.buildSerial=<提交时间 Unix 秒>），
// 在线升级只接受 serial 更大的发布。本地构建保持 "0"：节点进程不声明 upgrade_v1，助手拒绝一切升级。
var buildSerial = "0"

// buildSerialValue 解析 buildSerial；不是十进制正整数时按 0（dev）处理。
func buildSerialValue() uint64 {
	n, err := strconv.ParseUint(strings.TrimSpace(buildSerial), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// upgradeConfig 是 config.json 的 upgrade 段。
type upgradeConfig struct {
	// AllowHTTP 允许经明文 http 下载升级包，只给开发与验收环境（回环上的假面板）；生产不要开。
	AllowHTTP bool `json:"allow_http"`
}

// upgradeEnv 是判断本机能否在线升级用到的事实，单独成结构便于测试。
type upgradeEnv struct {
	serial      uint64
	keyring     *upgrade.Keyring
	keyringErr  error
	exe         string // os.Executable() 的结果
	exeErr      error
	pathUnitErr error // Stat(upgrade.DefaultPathUnit) 的错误
}

func currentUpgradeEnv() upgradeEnv {
	env := upgradeEnv{serial: buildSerialValue()}
	env.keyring, env.keyringErr = upgrade.BuiltinKeyring()
	env.exe, env.exeErr = os.Executable()
	_, env.pathUnitErr = os.Stat(upgrade.DefaultPathUnit)
	return env
}

// upgradeUnsupported 返回本机不能在线升级的原因，空串即可以。四个前提缺一不可：
// 发布构建（serial > 0）、内置了公钥、二进制装在助手会替换的固定路径、root 助手的 path 单元已装。
// 缺任何一个都不声明 upgrade_v1，面板就不会给这台发升级指令（后台显示「不支持在线升级」）。
func (e upgradeEnv) upgradeUnsupported() string {
	switch {
	case e.serial == 0:
		return "开发构建（buildSerial=0）"
	case e.keyringErr != nil:
		return "内置公钥无法加载：" + e.keyringErr.Error()
	case e.keyring.Len() == 0:
		return "本构建没有内置升级公钥"
	case e.exeErr != nil:
		return "取不到本进程二进制路径"
	case filepath.Clean(e.exe) != upgrade.DefaultBinPath:
		return "二进制不在 " + upgrade.DefaultBinPath
	case e.pathUnitErr != nil:
		return "没装升级助手单元 " + upgrade.DefaultPathUnit
	}
	return ""
}

// newUpgrader 在本机满足前提时返回节点进程侧的 upgrade.Agent，否则返回 nil（接口零值，不是带类型的 nil 指针）。
func newUpgrader(cfg *config, env upgradeEnv, binarySHA string, log *slog.Logger) node.Upgrader {
	if why := env.upgradeUnsupported(); why != "" {
		log.Info("本机不支持在线升级", "原因", why)
		return nil
	}
	return &upgrade.Agent{
		Dir:          upgrade.DefaultStateDir,
		Keyring:      env.keyring,
		BuildSerial:  env.serial,
		GOARCH:       runtime.GOARCH,
		BinarySHA256: binarySHA,
		AllowHTTP:    cfg != nil && cfg.Upgrade.AllowHTTP,
		Log:          log,
	}
}

// sessionFeatures 是 Hello 里声明的可选能力：名单增量总有，在线升级只在 newUpgrader 给出实现时才有。
func sessionFeatures(up node.Upgrader) []string {
	features := []string{panel.FeatureUsersDelta}
	if up != nil {
		features = append(features, panel.FeatureUpgrade)
	}
	return features
}

// upgradeApplyCommand 是 root 侧升级助手（pandora-native-upgrade.service 的 ExecStart）。
//
// 不收任何参数：路径全用规格里的固定值，request 里的内容一概不当路径用。公钥与 buildSerial
// 都取自正在运行的这个 root 属主二进制本身，不取暂存目录里的任何东西。
func upgradeApplyCommand(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("upgrade-apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("upgrade-apply 不收参数: %s", strings.Join(fs.Args(), " "))
	}
	if os.Geteuid() != 0 {
		return errors.New("upgrade-apply 只能以 root 运行（由 pandora-native-upgrade.service 触发）")
	}
	keyring, err := upgrade.BuiltinKeyring()
	if err != nil {
		return fmt.Errorf("内置公钥无法加载: %w", err)
	}
	log := installLogger("info", stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	h := &upgrade.Helper{
		Keyring:     keyring,
		BuildSerial: buildSerialValue(),
		GOARCH:      runtime.GOARCH,
		Service:     upgrade.Systemctl{},
		Log:         log,
	}
	return h.Run(ctx)
}
