package dbbackup

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// 本地备份封条。
//
// 没配 WebDAV 时备份只在本机，没有签名清单（清单与可信检查点是给异地副本防替换、防回滚的）。
// backup-postgres.sh 给每份本地备份另写一份封条 aegis-postgres-<时间>.dump.age.seal：
// 用这台安装的 age 解密私钥（AEGIS_BACKUP_AGE_IDENTITY）派生的 HMAC，签「归档文件名、归档 SHA256、
// 归档字节数、校验文件 SHA256」。
//
// 信任根就是这把私钥：恢复本来就离不开它（解密），机器整体丢失时要另存的也只有它，不另造密钥。
// age 的收件人（公钥）写在 .env 里、不是秘密，任何人都能加密出一份「解得开」的归档；封条要求持有私钥，
// 所以外来的归档、被换掉的归档或校验文件都过不了。它不防回滚（本机任何一份留存期内的备份都能恢复），
// 也不防拿到 root 的人——那个人本来就能读私钥。
const LocalSealSchema = "PANDORA-LOCAL-SEAL-V1"

const localSealKeyLabel = "pandora-local-backup-seal-v1"

// localSealKey 从规范化后的 age 私钥（那一行 AGE-SECRET-KEY-1…）派生封条密钥（HMAC-SHA256，固定标签），
// 私钥本身不直接当 MAC 密钥用。派生结果由 TestLocalSealKeyKnownAnswer 钉住：改了它，留存期内的旧封条全部失效
func localSealKey(identityLine []byte) []byte {
	m := hmac.New(sha256.New, identityLine)
	_, _ = m.Write([]byte(localSealKeyLabel))
	return m.Sum(nil)
}

func localSealMAC(key []byte, pair LocalPair) string {
	m := hmac.New(sha256.New, key)
	_, _ = fmt.Fprintf(m, "%s\n%s\n%s\n%d\n%s\n", LocalSealSchema, pair.Archive.Name, pair.Archive.SHA256,
		pair.Archive.Bytes, pair.Checksum.SHA256)
	return hex.EncodeToString(m.Sum(nil))
}

// parseAgeSecretKey 从 age 私钥文件里取出唯一的 AGE-SECRET-KEY-1… 行并规范化：去首尾空白、转大写（Bech32 不分大小写）。
// age-keygen 写的「# created」「# public key」注释、空行、CRLF、有没有末尾换行都不影响结果——另存私钥时只留那一行、
// 丢了换行，age 照样能解密，封条也必须照样认。没有密钥行、多于一行、混进别的内容（如插件身份）都报错
func parseAgeSecretKey(raw []byte) ([]byte, error) {
	var found []byte
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		if !strings.HasPrefix(upper, "AGE-SECRET-KEY-1") || strings.ContainsAny(upper[len("AGE-SECRET-KEY-1"):], " \t") {
			return nil, errors.New("备份解密私钥文件里有认不出的内容（只认一行 AGE-SECRET-KEY-1…）")
		}
		if found != nil {
			return nil, errors.New("备份解密私钥文件里有多把密钥，认不出用哪一把")
		}
		found = []byte(upper)
	}
	if found == nil {
		return nil, errors.New("备份解密私钥文件里没有 AGE-SECRET-KEY-1… 那一行")
	}
	return found, nil
}

func loadSealKey(identityPath string) ([]byte, error) {
	identity, err := readPrivateFile(identityPath, 64<<10)
	if err != nil {
		return nil, errors.New("读不了备份解密私钥（要 root 所有、0600、单链接）")
	}
	line, err := parseAgeSecretKey(identity)
	if err != nil {
		return nil, err
	}
	return localSealKey(line), nil
}

// SealLocalBackup 核过归档与校验文件（SHA256 对得上、路径与权限合规）之后，在归档旁写 <归档>.seal（0600，
// 已存在就拒绝），返回封条路径
func SealLocalBackup(archive, checksum, identityPath string) (string, error) {
	pair, err := VerifyLocalPair(archive, checksum)
	if err != nil {
		return "", err
	}
	key, err := loadSealKey(identityPath)
	if err != nil {
		return "", err
	}
	sealPath := archive + ".seal"
	parent, err := validateSecureParent(sealPath)
	if err != nil {
		return "", err
	}
	line := LocalSealSchema + " " + localSealMAC(key, pair) + "\n"
	f, err := os.OpenFile(sealPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", errors.New("封条已存在或无法创建")
	}
	if _, err := io.WriteString(f, line); err != nil {
		_ = f.Close()
		_ = os.Remove(sealPath)
		return "", errors.New("写入封条失败")
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(sealPath)
		return "", errors.New("同步封条失败")
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(sealPath)
		return "", errors.New("关闭封条失败")
	}
	if err := syncCheckpointDirectory(parent); err != nil {
		return "", errors.New("持久化封条失败")
	}
	return sealPath, nil
}

// VerifyLocalSeal 核本地备份：归档与校验文件对得上，封条是 <归档>.seal、格式对、HMAC 与这台安装的私钥算出来的一致
func VerifyLocalSeal(archive, checksum, sealPath, identityPath string) error {
	if filepath.Clean(sealPath) != filepath.Clean(archive+".seal") {
		return errors.New("封条路径必须是 <归档>.seal")
	}
	pair, err := VerifyLocalPair(archive, checksum)
	if err != nil {
		return err
	}
	raw, err := readPrivateFile(sealPath, 256)
	if err != nil {
		return errors.New("读不了封条（要 root 所有、0600、单链接）")
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 || fields[0] != LocalSealSchema || len(fields[1]) != sha256HexLength {
		return errors.New("封条格式无效")
	}
	got, err := hex.DecodeString(fields[1])
	if err != nil {
		return errors.New("封条格式无效")
	}
	key, err := loadSealKey(identityPath)
	if err != nil {
		return err
	}
	want, _ := hex.DecodeString(localSealMAC(key, pair))
	if !hmac.Equal(got, want) {
		return errors.New("封条对不上：这份备份不是本安装写出的，或归档、校验文件被改过")
	}
	return nil
}
