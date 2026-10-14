---
paths:
  - "panel/internal/pkg/dbbackup/**"
  - "panel/internal/app/backup.go"
---

# 数据库备份（dbbackup 与 pandora backup）

## 入口与配置

- CLI 在 `panel/internal/app/backup.go`：`pandora backup` 子命令（`dir`、`seal-local`、`verify-local-seal`、`webdav-upload`、`init-signing-key`、`verify-manifest`）。除 `dir` 外都要求 root（`dbbackup.RequireRootRuntime`）。
- `webdav-upload` 总时限 `webdavUploadTimeout`（45 分钟）；失败只报阶段名，不带远端地址与凭据（`runWebDAVUpload`）。
- `loadBackupDir` 只读 `backup.dir`（viper / `PANDORA_BACKUP_DIR`），**不走**完整 `config.Load`（`backup.go` 注释）。
- WebDAV 配置缺省路径 `dbbackup.DefaultWebDAVConfigPath`；`LoadWebDAVRuntimeConfig` 读 JSON 后校验签名密钥、检查点、Hook 均为绝对路径且互不相同（`config.go`）。

## 私密文件：openSecureRegular

- WebDAV 口令、清单签名种子、检查点、本地归档与 `.sha256` 等，一律经 `openSecureRegular` / `readPrivateFile`（`config.go`）；**不要**对同类输入另写 `os.Open`。
- 打开前 `validateSecureParent`：父目录属主、权限、不经符号链接；打开后核对普通文件、属主、权限、单硬链接、打开期间路径未变（`openSecureRegular`）。
- **Linux**：`file_owner_linux.go`——可信属主 `secureFileOwnerUID`（生产为 root）；`file_open_linux.go` 用 `O_NOFOLLOW` 只读打开。
- **非 Linux**：`file_owner_other.go` / `file_open_other.go` 对属主/硬链接/父目录等为空实现；`RequireRootRuntime` 直接报「只支持 Linux」。**macOS 上测过不能代表 Linux 权限行为**；Linux 专测见 `file_owner_linux_test.go`（`secureTempDir`、改写 `secureFileOwnerUID`）。
- `secureFileOwnerUID`、`checkpointHookRoot` 仅为同包测试改写，不经配置、环境变量或导出接口暴露（`file_owner_linux.go`、`checkpoint_hook.go` 注释）。

## 检查点复制 Hook

- Hook 路径必须在固定根 `checkpointHookRoot`（生产 `/opt/pandora/checkpoint-sink`）；`validateCheckpointHookPath` / `ReplicateTrustedCheckpoint` 拒绝其它目录——**不可配置到任意路径**（能配路径等于能执行任意代码）（`checkpoint_hook.go`）。
- 执行前 `openSecureRegular` 读 Hook 与检查点；Hook 须可执行；回执须匹配 `AEPB-CHECKPOINT-RECEIPT-V1` + 检查点 SHA256（`ReplicateTrustedCheckpoint`）。

## 本地对与 WebDAV 上传

- `VerifyLocalPair`：归档与 `归档.sha256` 同目录、命名符合 `backupObjectName`；校验文件内容 SHA256 后再读归档（`config.go`）。
- `BeginManifestTransaction`：在检查点 `.lock` 上准备签名清单；`Commit` 原子写本地可信检查点；上传顺序见 `runWebDAVUpload`（探测 → 归档 → checksum → manifest → `Commit` → `ReplicateTrustedCheckpoint`）。
- `WebDAVClient`：对象名只接受 `pandora-postgres-*.dump.age` / `.sha256` / `.manifest.json` 后缀；发布用临时 `.partial.<uuid>` 再 `MOVE`，`Overwrite: F`；上传后按摘要回读校验（`webdav.go`）。

## 签名清单链

- `manifest.go`：`BuildSignedManifest` 链式 `sequence` 与 `previous_manifest_sha256`；同一 `backup_id` 制品冲突报错；`ParseAndVerifyManifest` / `VerifyRecoveryBundle` 用于恢复核验。
- `init-signing-key` 用 `O_EXCL` 创建种子文件，不覆盖已有（`InitializeManifestSigningKey`）。
