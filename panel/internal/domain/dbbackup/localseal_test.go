package dbbackup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 写一份本地备份对（归档 + 校验文件）与一把 age 私钥（内容是虚构的）
func writeSealFixture(t *testing.T, dir, backupID string, payload []byte) (archive, checksum string) {
	t.Helper()
	archive = filepath.Join(dir, "aegis-postgres-"+backupID+".dump.age")
	checksum = archive + ".sha256"
	if err := os.WriteFile(archive, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if err := os.WriteFile(checksum, []byte(hex.EncodeToString(sum[:])+"  "+filepath.Base(archive)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return archive, checksum
}

func writeIdentity(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLocalSealRoundTripAndRejections(t *testing.T) {
	sealTestTrustCurrentUser(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	keys := t.TempDir()
	// t.TempDir 的子目录按 0777 减 umask 建（多半是 0755），私密文件的父目录要 0700
	if err := os.Chmod(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	identity := writeIdentity(t, keys, "backup-age.key", "AGE-SECRET-KEY-1FIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREFIXTUREXX\n")
	other := writeIdentity(t, keys, "other-age.key", "AGE-SECRET-KEY-1OTHEROTHEROTHEROTHEROTHEROTHEROTHEROTHEROTHEROTHEROTHERXX\n")
	archive, checksum := writeSealFixture(t, dir, "20261009T142951Z", []byte("encrypted-archive-bytes"))

	seal, err := SealLocalBackup(archive, checksum, identity)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if seal != archive+".seal" {
		t.Fatalf("seal path = %s", seal)
	}
	if info, err := os.Stat(seal); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("seal must be 0600: %v %v", info, err)
	}
	if err := VerifyLocalSeal(archive, checksum, seal, identity); err != nil {
		t.Fatalf("own backup rejected: %v", err)
	}
	// 已有封条不覆盖
	if _, err := SealLocalBackup(archive, checksum, identity); err == nil {
		t.Fatal("an existing seal was overwritten")
	}
	// 别的私钥（外来的归档、换过私钥的机器）
	if err := VerifyLocalSeal(archive, checksum, seal, other); err == nil {
		t.Fatal("a seal from another identity was accepted")
	}
	// 封条路径必须是 <归档>.seal：内容对的封条放在别处也不认
	elsewhere := filepath.Join(dir, "x.seal")
	if raw, err := os.ReadFile(seal); err != nil || os.WriteFile(elsewhere, raw, 0o600) != nil {
		t.Fatal("copy seal")
	}
	if err := VerifyLocalSeal(archive, checksum, elsewhere, identity); err == nil {
		t.Fatal("a seal at another path was accepted")
	}
	// 归档被换（校验文件一起换成新归档的摘要）：封条签的是原摘要
	sealRaw, _ := os.ReadFile(seal)
	writeSealFixture(t, dir, "20261009T142951Z", []byte("a-different-archive"))
	if err := VerifyLocalSeal(archive, checksum, seal, identity); err == nil ||
		!strings.Contains(err.Error(), "封条对不上") {
		t.Fatalf("a replaced archive was accepted: %v", err)
	}
	// 封条被改一位
	writeSealFixture(t, dir, "20261009T142951Z", []byte("encrypted-archive-bytes"))
	tampered := []byte(strings.Replace(string(sealRaw), LocalSealSchema+" ", LocalSealSchema+" f", 1))
	tampered = append(tampered[:len(LocalSealSchema)+1+64], '\n')
	if err := os.WriteFile(seal, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLocalSeal(archive, checksum, seal, identity); err == nil {
		t.Fatal("a tampered seal was accepted")
	}
	// 格式不对
	if err := os.WriteFile(seal, []byte("PANDORA-LOCAL-SEAL-V0 00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLocalSeal(archive, checksum, seal, identity); err == nil {
		t.Fatal("a malformed seal was accepted")
	}
	// 没有封条
	if err := os.Remove(seal); err != nil {
		t.Fatal(err)
	}
	if err := VerifyLocalSeal(archive, checksum, seal, identity); err == nil {
		t.Fatal("a missing seal was accepted")
	}
}

// 封条签的内容固定：文件名、归档摘要、字节数、校验文件摘要，任何一项变了 MAC 都变
func TestLocalSealCoversNameSizeAndChecksum(t *testing.T) {
	key := localSealKey([]byte("identity"))
	base := manifestTestPair("20261009T142951Z")
	want := localSealMAC(key, base)
	for name, mutate := range map[string]func(p *LocalPair){
		"name":     func(p *LocalPair) { p.Archive.Name = "aegis-postgres-20261009T142952Z.dump.age" },
		"sha":      func(p *LocalPair) { p.Archive.SHA256 = strings.Repeat("0", 64) },
		"bytes":    func(p *LocalPair) { p.Archive.Bytes++ },
		"checksum": func(p *LocalPair) { p.Checksum.SHA256 = strings.Repeat("1", 64) },
	} {
		p := base
		mutate(&p)
		if localSealMAC(key, p) == want {
			t.Fatalf("the seal does not cover %s", name)
		}
	}
	if localSealMAC(localSealKey([]byte("other")), base) == want {
		t.Fatal("the seal does not depend on the identity")
	}
}
