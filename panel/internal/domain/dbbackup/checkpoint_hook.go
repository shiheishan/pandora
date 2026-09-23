package dbbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
)

// checkpointHookRoot 是检查点复制 Hook 唯一允许存放的目录。
//
// 用 var 而不是 const，只是为了让同包测试能把它指向临时目录 ——
// 它是包私有的，外部代码无从修改，生产行为与写死常量完全一致。
// 之所以要写死一个根目录：Hook 是会被执行的东西，如果允许配置文件
// 指定任意路径，那么改配置的人就等同于能执行任意代码。
var checkpointHookRoot = "/opt/aegispanel/checkpoint-sink"

func ReplicateTrustedCheckpoint(ctx context.Context, hookPath, checkpointPath string) error {
	if filepath.Clean(filepath.Dir(hookPath)) != checkpointHookRoot {
		return errors.New("独立检查点复制 Hook 必须位于固定受保护目录")
	}
	hook, info, err := openSecureRegular(hookPath, 16<<20, true)
	if err != nil {
		return errors.New("独立检查点复制 Hook 不安全或无法读取")
	}
	defer hook.Close()
	if info.Mode().Perm()&0o100 == 0 {
		return errors.New("独立检查点复制 Hook 不可执行")
	}
	checkpoint, _, err := openSecureRegular(checkpointPath, maxManifestBytes, true)
	if err != nil {
		return errors.New("本地可信检查点无效")
	}
	raw, err := io.ReadAll(io.LimitReader(checkpoint, maxManifestBytes+1))
	_ = checkpoint.Close()
	if err != nil || len(raw) == 0 || len(raw) > maxManifestBytes {
		return errors.New("读取本地可信检查点失败")
	}
	sum := sha256.Sum256(raw)
	wantReceipt := "AEPB-CHECKPOINT-RECEIPT-V1 " + hex.EncodeToString(sum[:]) + "\n"
	receipt, err := executeTrustedCheckpointHook(ctx, hook, checkpointPath)
	if err != nil || receipt != wantReceipt {
		return errors.New("独立可信检查点复制失败")
	}
	return nil
}
