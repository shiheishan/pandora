package app

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/dbbackup"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// defaultBackupDir 与 internal/pkg/config 里 backup.dir 的缺省值相同（系统状态页读同一个目录）。
const defaultBackupDir = "/var/backups/pandora"

// webdavUploadTimeout 是一次异地上传（探测、归档、校验文件、清单、检查点复制）的总时限。
const webdavUploadTimeout = 45 * time.Minute

// 以下几处在单测里替换：root 检查与 WebDAV 上传流程。
var (
	backupRequireRoot = dbbackup.RequireRootRuntime
	backupWebDAVRun   = runWebDAVUpload
)

// newBackupCmd 是 pandora backup：部署脚本（backup-postgres.sh、verify-backup.sh）用的本地工具，
// 封条、核验与异地上传都在这里。只用自己的最小配置（backup.dir 与 WebDAV 配置文件），不读完整配置、不连库与 Redis。
// 除 dir 外都要 root：它们读写 root 独占的私钥、检查点与备份文件。
func newBackupCmd(configPath *string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "数据库备份的本地工具（dir、seal-local、verify-local-seal、webdav-upload、init-signing-key、verify-manifest）",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "dir",
		Short: "打印配置的备份目录（backup.dir，配置文件或 PANDORA_BACKUP_DIR）",
		Args:  backupArgs(0, "pandora backup dir"),
		RunE: func(c *cobra.Command, _ []string) error {
			dir, err := loadBackupDir(*configPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), dir)
			return nil
		},
	})

	// 本地备份封条（没配 WebDAV 时的本地备份，见 dbbackup.LocalSealSchema）：backup-postgres.sh 写，verify-backup.sh 核
	cmd.AddCommand(&cobra.Command{
		Use:   "seal-local <archive> <checksum> <age-identity> <age-recipient>",
		Short: "给本地备份写封条 <归档>.seal",
		Args:  backupArgs(4, "pandora backup seal-local <archive.dump.age> <archive.dump.age.sha256> <age-identity> <age-recipient>"),
		RunE: func(c *cobra.Command, args []string) error {
			if err := backupRequireRoot(); err != nil {
				return errors.New("local backup seal failed: root_required")
			}
			seal, err := dbbackup.SealLocalBackup(args[0], args[1], args[2], args[3])
			if err != nil {
				return fmt.Errorf("local backup seal failed: %w", err)
			}
			fmt.Fprintln(c.OutOrStdout(), "local backup sealed:", seal)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "verify-local-seal <archive> <checksum> <seal> <age-identity>",
		Short: "核本地备份的封条",
		Args:  backupArgs(4, "pandora backup verify-local-seal <archive.dump.age> <archive.dump.age.sha256> <seal> <age-identity>"),
		RunE: func(c *cobra.Command, args []string) error {
			if err := backupRequireRoot(); err != nil {
				return errors.New("local backup seal verification failed: root_required")
			}
			if err := dbbackup.VerifyLocalSeal(args[0], args[1], args[2], args[3]); err != nil {
				return fmt.Errorf("local backup seal verification failed: %w", err)
			}
			fmt.Fprintln(c.OutOrStdout(), "local backup seal verified")
			return nil
		},
	})

	var webdavConfig string
	upload := &cobra.Command{
		Use:   "webdav-upload <archive> <checksum>",
		Short: "把已加密、已校验的备份传到 WebDAV，并写签名清单与可信检查点",
		Args:  backupArgs(2, "pandora backup webdav-upload [--webdav-config <path>] <archive.dump.age> <archive.dump.age.sha256>"),
		RunE: func(c *cobra.Command, args []string) error {
			if err := backupRequireRoot(); err != nil {
				return errors.New("webdav backup upload failed: root_required")
			}
			ctx, cancel := context.WithTimeout(c.Context(), webdavUploadTimeout)
			defer cancel()
			summary, err := backupWebDAVRun(ctx, webdavConfig, args[0], args[1])
			if err != nil {
				return err
			}
			fmt.Fprintln(c.OutOrStdout(), summary)
			return nil
		},
	}
	upload.Flags().StringVar(&webdavConfig, "webdav-config", dbbackup.DefaultWebDAVConfigPath, "WebDAV 配置文件（root 所有、0600）")
	cmd.AddCommand(upload)

	cmd.AddCommand(&cobra.Command{
		Use:   "init-signing-key <seed-file>",
		Short: "生成备份清单签名密钥（不覆盖已有文件），打印公钥",
		Args:  backupArgs(1, "pandora backup init-signing-key <seed-file>"),
		RunE: func(c *cobra.Command, args []string) error {
			if err := backupRequireRoot(); err != nil {
				return errors.New("webdav backup key initialization failed: root_required")
			}
			publicKey, err := dbbackup.InitializeManifestSigningKey(args[0])
			if err != nil {
				return errors.New("webdav backup key initialization failed")
			}
			fmt.Fprintln(c.OutOrStdout(), publicKey)
			return nil
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "verify-manifest <archive> <checksum> <manifest> <public-key> <trusted-checkpoint>",
		Short: "核从 WebDAV 取回的备份：签名清单与 WebDAV 之外的可信检查点",
		Args:  backupArgs(5, "pandora backup verify-manifest <archive> <checksum> <manifest> <public-key> <trusted-checkpoint>"),
		RunE: func(c *cobra.Command, args []string) error {
			if err := backupRequireRoot(); err != nil {
				return errors.New("webdav backup verification failed: root_required")
			}
			if err := dbbackup.VerifyRecoveryBundle(args[0], args[1], args[2], args[3], args[4]); err != nil {
				return errors.New("webdav backup verification failed")
			}
			fmt.Fprintln(c.OutOrStdout(), "webdav backup manifest verified")
			return nil
		},
	})

	return cmd
}

// backupArgs 要求恰好 n 个参数；不对时以退出码 2 结束并打印用法（与旧工具同口径）。
func backupArgs(n int, usage string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return &exitCodeError{code: 2, msg: "usage: " + usage}
		}
		return nil
	}
}

// runWebDAVUpload 是 webdav-upload 的完整流程：核本地归档与校验文件 → 读 WebDAV 配置 → 准备清单事务 →
// 探测 → 传归档、校验文件、清单（各自核摘要）→ 提交检查点 → 交独立 hook 复制检查点。
// 失败只报阶段名（不带远端地址、凭据与底层错误），与旧 aegis-backup-webdav 的报错一致。
func runWebDAVUpload(ctx context.Context, configPath, archive, checksum string) (string, error) {
	fail := func(stage string) error { return errors.New("webdav backup upload failed: " + stage) }
	pair, err := dbbackup.VerifyLocalPair(archive, checksum)
	if err != nil {
		return "", fail("local_pair_invalid")
	}
	runtimeConfig, err := dbbackup.LoadWebDAVRuntimeConfig(ctx, configPath)
	if err != nil {
		return "", fail("target_invalid")
	}
	manifestTx, err := dbbackup.BeginManifestTransaction(pair, runtimeConfig.ManifestSigningKeyFile, runtimeConfig.ManifestCheckpointFile)
	if err != nil {
		return "", fail("manifest_prepare_failed")
	}
	defer manifestTx.Close()
	client := dbbackup.NewWebDAVClient(runtimeConfig.Target)
	if err := client.Probe(ctx); err != nil {
		return "", fail("probe_failed")
	}
	archiveResult, err := client.UploadVerifiedDigest(ctx, archive, filepath.Base(archive), pair.ArchiveSHA256)
	if err != nil {
		return "", fail("archive_failed")
	}
	if _, err := client.UploadVerifiedDigest(ctx, checksum, filepath.Base(checksum), pair.ChecksumSHA256); err != nil {
		return "", fail("checksum_failed")
	}
	if _, err := client.UploadVerifiedBytes(ctx, manifestTx.ObjectName, manifestTx.Bytes); err != nil {
		return "", fail("manifest_failed")
	}
	if err := manifestTx.Commit(); err != nil {
		return "", fail("checkpoint_failed")
	}
	if err := dbbackup.ReplicateTrustedCheckpoint(ctx, runtimeConfig.CheckpointReplicationHook,
		runtimeConfig.ManifestCheckpointFile); err != nil {
		return "", fail("checkpoint_replication_failed")
	}
	return fmt.Sprintf("webdav backup upload complete: archive=%s bytes=%d sha256=%s",
		archiveResult.ObjectName, archiveResult.Bytes, archiveResult.SHA256), nil
}

// loadBackupDir 只读 backup.dir：配置文件（--config）里的值，PANDORA_BACKUP_DIR 覆盖，都没有用缺省。
// 不走 config.Load：那会要求数据库、Redis 与各把密钥，备份单元的环境里没有这些。
func loadBackupDir(configPath string) (string, error) {
	v := viper.New()
	v.SetEnvPrefix("PANDORA")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()
	v.SetDefault("backup.dir", defaultBackupDir)
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return "", fmt.Errorf("读取配置文件: %w", err)
		}
	}
	dir := strings.TrimSpace(v.GetString("backup.dir"))
	switch {
	case dir == "":
		return "", errors.New("backup.dir 为空：没有配置备份目录")
	case !path.IsAbs(dir) || path.Clean(dir) != dir:
		return "", fmt.Errorf("backup.dir 必须是规范的绝对路径：%s", dir)
	case dir == "/":
		return "", errors.New("backup.dir 不能是 /")
	}
	return dir, nil
}
