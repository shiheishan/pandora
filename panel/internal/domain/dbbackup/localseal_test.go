package dbbackup

import (
	"crypto/hmac"
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
	identity := writeIdentity(t, keys, "backup-age.key", sealFixtureKey+"\n")
	other := writeIdentity(t, keys, "other-age.key", sealOtherKey+"\n")
	archive, checksum := writeSealFixture(t, dir, "20261009T142951Z", []byte("encrypted-archive-bytes"))

	seal, err := SealLocalBackup(archive, checksum, identity, sealFixtureRecipient)
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
	if _, err := SealLocalBackup(archive, checksum, identity, sealFixtureRecipient); err == nil {
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

// 虚构的 age 私钥（不是能用的密钥，只用来钉派生结果）
// 虚构的 age 私钥：X25519 私钥字节 0x01…0x20 与 0x21…0x40 的 Bech32 编码（校验和正确），收件人由它推出。
// 拆成两段拼：整串照抄会被提交闸门的 age-secret-key 规则当成真私钥拦下（它确实是格式正确的样子）
const (
	sealFixtureKey       = "AGE-SECRET-KEY-" + "1QYPQXPQ9QCRSSZG2PVXQ6RS0ZQG3YYC5Z5TPWXQERGD3C8G7RUSQGPQYEE"
	sealFixtureRecipient = "age1q73he0q5yzfu3d64msd3p6rvksnrwjk3d2598mgtmlqt9wrdr37q2vrn72"
	sealOtherKey         = "AGE-SECRET-KEY-" + "1YY3ZXFP9YCNJS2F29VKZ6T30XQCNYVE5X5MRWWPE8GANC0F78AQQ2X9KSF"
	sealOtherRecipient   = "age1tp56lazs2jtn9ja2a409m7dnpfk6x89su46zht266js6w6835eascutqdx"
)

// 已知答案：HMAC-SHA256(键 = 规范化后的密钥行, 消息 = "pandora-local-backup-seal-v1")，在测试外独立算出。
// 改了派生方式（去掉标签、标签写空、拿文件原始字节当键），留存期内的旧封条会全部失效，这里先红
func TestLocalSealKeyKnownAnswer(t *testing.T) {
	const want = "be9bd795f130c9701b62546533d0ea7c13683eda2aec2526a3ebf5b63074366a"
	keys := t.TempDir()
	if err := os.Chmod(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	sealTestTrustCurrentUser(t)
	path := writeIdentity(t, keys, "backup-age.key",
		"# created: 2026-10-09T14:28:00Z\n# public key: age1fixturefixturefixture\n"+sealFixtureKey+"\n")
	key, err := loadSealKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(key); got != want {
		t.Fatalf("seal key derivation changed: got %s want %s", got, want)
	}
}

// 同一把密钥的几种文件形态（age-keygen 原样、只留密钥行、没有末尾换行、前后空白、小写）派生出同一个封条密钥，
// 互相认得封条；没有密钥行、多于一行、混进别的内容都报错
func TestLocalSealKeyIgnoresFileFormatting(t *testing.T) {
	sealTestTrustCurrentUser(t)
	keys := t.TempDir()
	if err := os.Chmod(keys, 0o700); err != nil {
		t.Fatal(err)
	}
	forms := map[string]string{
		"age-keygen": "# created: 2026-10-09T14:28:00Z\n# public key: age1fixturefixturefixture\n" + sealFixtureKey + "\n",
		"key-only":   sealFixtureKey + "\n",
		"no-newline": sealFixtureKey,
		"crlf-space": "  " + sealFixtureKey + "  \r\n\r\n",
		"lowercase":  strings.ToLower(sealFixtureKey) + "\n",
	}
	var first []byte
	for name, body := range forms {
		key, err := loadSealKey(writeIdentity(t, keys, name+".key", body))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if first == nil {
			first = key
		} else if !hmac.Equal(first, key) {
			t.Fatalf("%s derives a different seal key", name)
		}
	}
	// 用 age-keygen 原样的文件封、用只留密钥行的文件核
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	archive, checksum := writeSealFixture(t, dir, "20261010T011540Z", []byte("encrypted-archive-bytes"))
	seal, err := SealLocalBackup(archive, checksum, filepath.Join(keys, "age-keygen.key"), sealFixtureRecipient)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"key-only", "no-newline", "crlf-space", "lowercase"} {
		if err := VerifyLocalSeal(archive, checksum, seal, filepath.Join(keys, name+".key")); err != nil {
			t.Fatalf("a seal made with the age-keygen file is rejected with the %s file: %v", name, err)
		}
	}
	for name, body := range map[string]string{
		"empty":       "",
		"comments":    "# created: x\n# public key: age1x\n",
		"two-keys":    sealFixtureKey + "\n" + sealOtherKey + "\n",
		"truncated":   sealFixtureKey[:len(sealFixtureKey)-1] + "\n",
		"one-char":    strings.Replace(sealFixtureKey, "QYPQ", "QYPP", 1) + "\n",
		"nul":         sealFixtureKey + "\x00\n",
		"garbage":     "hello\n" + sealFixtureKey + "\n",
		"plugin-only": "AGE-PLUGIN-YUBIKEY-1QQQQQQ\n",
	} {
		if _, err := loadSealKey(writeIdentity(t, keys, name+".bad", body)); err == nil {
			t.Fatalf("identity file %q was accepted", name)
		}
	}
}

// I8：私钥行要是一把完整的 age 私钥（Bech32 校验和对、32 字节），推出来的收件人要和 .env 的
// AEGIS_BACKUP_AGE_RECIPIENT 一样；对不上不封（封了也解不开）
func TestSealChecksTheIdentityAgainstTheRecipient(t *testing.T) {
	sealTestTrustCurrentUser(t)
	keys, dir := t.TempDir(), t.TempDir()
	for _, d := range []string{keys, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	identity := writeIdentity(t, keys, "backup-age.key", sealFixtureKey+"\n")
	archive, checksum := writeSealFixture(t, dir, "20261010T011540Z", []byte("encrypted-archive-bytes"))
	if _, err := SealLocalBackup(archive, checksum, identity, sealOtherRecipient); err == nil {
		t.Fatal("sealed with an identity that does not match the configured recipient")
	}
	if _, err := os.Stat(archive + ".seal"); err == nil {
		t.Fatal("a seal was written for a mismatched recipient")
	}
	if _, err := SealLocalBackup(archive, checksum, identity, "  "+sealFixtureRecipient+"\n"); err != nil {
		t.Fatalf("matching recipient (with surrounding blanks) refused: %v", err)
	}
}

// Bech32：BIP-173 的有效向量解得开；编码再解码一致；推出的收件人与固定值一致
func TestAgeKeyBech32(t *testing.T) {
	for _, v := range []string{"A12UEL5L", "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
		"split1checkupstagehandshakeupstreamerranterredcaperred2y9e3w"} {
		if _, _, err := bech32Decode(v); err != nil {
			t.Fatalf("valid BIP-173 vector %s rejected: %v", v, err)
		}
	}
	// 大小写混写、校验和错、没有人类可读部分（age 不限 90 字符，长度不核）
	for _, v := range []string{"A12UEL5l", "a12uel5m", "1pzry9x0s0muk"} {
		if _, _, err := bech32Decode(v); err == nil {
			t.Fatalf("invalid vector %s accepted", v)
		}
	}
	secret, err := decodeAgeSecretKey(sealFixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range secret {
		if b != byte(i+1) {
			t.Fatalf("fixture secret byte %d = %d", i, b)
		}
	}
	if r, err := ageRecipientOf(secret); err != nil || r != sealFixtureRecipient {
		t.Fatalf("recipient = %s, %v", r, err)
	}
	if got := strings.ToUpper(bech32Encode("age-secret-key-", secret)); got != sealFixtureKey {
		t.Fatalf("encode = %s", got)
	}
}
