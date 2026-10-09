package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/aegispanel/aegis/internal/domain/dbbackup"
	"github.com/aegispanel/aegis/internal/platform/config"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 2 && args[0] == "init-signing-key" {
		if err := dbbackup.RequireRootRuntime(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "webdav backup key initialization failed: root_required")
			return 1
		}
		publicKey, err := dbbackup.InitializeManifestSigningKey(args[1])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "webdav backup key initialization failed")
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, publicKey)
		return 0
	}
	if len(args) == 6 && args[0] == "verify-manifest" {
		if err := dbbackup.RequireRootRuntime(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "webdav backup verification failed: root_required")
			return 1
		}
		if err := dbbackup.VerifyRecoveryBundle(args[1], args[2], args[3], args[4], args[5]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "webdav backup verification failed")
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, "webdav backup manifest verified")
		return 0
	}
	// 本地备份封条（没配 WebDAV 时的本地备份，见 dbbackup.LocalSealSchema）：backup-postgres.sh 写，verify-backup.sh 核
	if len(args) == 4 && args[0] == "seal-local" {
		if err := dbbackup.RequireRootRuntime(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "local backup seal failed: root_required")
			return 1
		}
		seal, err := dbbackup.SealLocalBackup(args[1], args[2], args[3])
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "local backup seal failed:", err)
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, "local backup sealed:", seal)
		return 0
	}
	if len(args) == 5 && args[0] == "verify-local-seal" {
		if err := dbbackup.RequireRootRuntime(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "local backup seal verification failed: root_required")
			return 1
		}
		if err := dbbackup.VerifyLocalSeal(args[1], args[2], args[3], args[4]); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "local backup seal verification failed:", err)
			return 1
		}
		_, _ = fmt.Fprintln(os.Stdout, "local backup seal verified")
		return 0
	}
	if len(args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: aegis-backup-webdav <archive.dump.age> <archive.dump.age.sha256> | init-signing-key <seed-file> | verify-manifest <archive> <checksum> <manifest> <public-key> <trusted-checkpoint> | seal-local <archive> <checksum> <age-identity> | verify-local-seal <archive> <checksum> <seal> <age-identity>")
		return 2
	}
	if err := dbbackup.RequireRootRuntime(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: root_required")
		return 1
	}
	archive, checksum := args[0], args[1]
	pair, err := dbbackup.VerifyLocalPair(archive, checksum)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: local_pair_invalid")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	// 只要这一项，不走要求数据库与主密钥的 config.Load
	runtimeConfig, err := dbbackup.LoadWebDAVRuntimeConfig(ctx, config.LoadBackupWebDAV().ConfigPath)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: target_invalid")
		return 1
	}
	manifestTx, err := dbbackup.BeginManifestTransaction(pair, runtimeConfig.ManifestSigningKeyFile, runtimeConfig.ManifestCheckpointFile)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: manifest_prepare_failed")
		return 1
	}
	defer manifestTx.Close()
	client := dbbackup.NewWebDAVClient(runtimeConfig.Target)
	if err := client.Probe(ctx); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: probe_failed")
		return 1
	}
	archiveResult, err := client.UploadVerifiedDigest(ctx, archive, filepath.Base(archive), pair.ArchiveSHA256)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: archive_failed")
		return 1
	}
	if _, err := client.UploadVerifiedDigest(ctx, checksum, filepath.Base(checksum), pair.ChecksumSHA256); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: checksum_failed")
		return 1
	}
	if _, err := client.UploadVerifiedBytes(ctx, manifestTx.ObjectName, manifestTx.Bytes); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: manifest_failed")
		return 1
	}
	if err := manifestTx.Commit(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: checkpoint_failed")
		return 1
	}
	if err := dbbackup.ReplicateTrustedCheckpoint(ctx, runtimeConfig.CheckpointReplicationHook,
		runtimeConfig.ManifestCheckpointFile); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "webdav backup upload failed: checkpoint_replication_failed")
		return 1
	}
	_, _ = fmt.Fprintf(os.Stdout, "webdav backup upload complete: archive=%s bytes=%d sha256=%s\n",
		archiveResult.ObjectName, archiveResult.Bytes, archiveResult.SHA256)
	return 0
}
