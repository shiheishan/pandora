// Command signmanifest 为 pdnd 发布清单做 ed25519 签名、用本仓库内置的公钥核对签名，或生成新的签名密钥对。
//
// 私钥只从 -key 指定的文件读取，不接受命令行明文密钥；generate 写出的私钥只应放进维护者的密码管理器与
// GitHub environment secret（release-signing / PDND_SIGNING_KEY），不要长期留在构建机上。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aegispanel/nodeagent/upgrade"
)

const maxManifestBytes = 64 << 10 // 与 upgrade.MaxManifestBytes 一致

// manifestHeader 用于签名前对清单做最小合法性核对。
type manifestHeader struct {
	Schema    int                        `json:"schema"`
	Serial    uint64                     `json:"serial"`
	Artifacts map[string]json.RawMessage `json:"artifacts"`
}

// sigJSON 是 pdnd-manifest.json.sig 的磁盘格式。
type sigJSON struct {
	KeyID string `json:"key_id"`
	Sig   string `json:"sig"`
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "signmanifest:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("signmanifest", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var keyPath, inPath, outPath, genPrefix, verifySig string
	fs.StringVar(&keyPath, "key", "", "ed25519 私钥文件（一行 base64）")
	fs.StringVar(&inPath, "in", "", "待签名的 pdnd-manifest.json")
	fs.StringVar(&outPath, "out", "", "输出的 pdnd-manifest.json.sig")
	fs.StringVar(&genPrefix, "gen", "", "生成密钥对，文件前缀（写出 <前缀>.key 与 <前缀>.pub）")
	fs.StringVar(&verifySig, "verify", "", "用 pdnd/upgrade/keys 里的公钥核对这份 .sig 与 -in 的清单（不需要私钥）")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return fmt.Errorf("未知参数: %s", strings.Join(fs.Args(), " "))
	}

	genSet := strings.TrimSpace(genPrefix) != ""
	keySet := strings.TrimSpace(keyPath) != ""
	inSet := strings.TrimSpace(inPath) != ""
	outSet := strings.TrimSpace(outPath) != ""
	verifySet := strings.TrimSpace(verifySig) != ""

	if genSet {
		if keySet || inSet || outSet || verifySet {
			return errors.New("-gen 不能与 -key、-in、-out、-verify 同时使用")
		}
		return generate(strings.TrimSpace(genPrefix), stdout)
	}
	if verifySet {
		if keySet || outSet || !inSet {
			return errors.New("-verify 只与 -in 一起使用")
		}
		ring, err := upgrade.BuiltinKeyring()
		if err != nil {
			return err
		}
		return verify(inPath, verifySig, ring, stdout)
	}

	if !keySet || !inSet || !outSet {
		return errors.New("签名需要同时指定 -key、-in、-out")
	}
	return sign(keyPath, inPath, outPath)
}

func generate(prefix string, stdout io.Writer) error {
	if prefix == "" {
		return errors.New("-gen 前缀不能为空")
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("生成密钥失败: %w", err)
	}

	keyFile := prefix + ".key"
	pubFile := prefix + ".pub"

	privLine := base64.StdEncoding.EncodeToString(priv) + "\n"
	if err := writeNewFile(keyFile, []byte(privLine), 0o600); err != nil {
		return err
	}

	pubLine := base64.StdEncoding.EncodeToString(pub) + "\n"
	if err := writeNewFile(pubFile, []byte(pubLine), 0o644); err != nil {
		_ = os.Remove(keyFile)
		return err
	}

	id := keyID(pub)
	_, _ = fmt.Fprintf(stdout, "key_id: %s\n", id)
	_, _ = fmt.Fprintf(stdout, "私钥文件: %s\n", keyFile)
	_, _ = fmt.Fprintf(stdout, "公钥文件: %s\n", pubFile)
	_, _ = fmt.Fprintln(stdout, "私钥放进密码管理器与 GitHub environment release-signing 的 secret PDND_SIGNING_KEY 后删掉本地文件；公钥放进 pdnd/upgrade/keys/")
	return nil
}

func writeNewFile(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("文件已存在，拒绝覆盖: %s", path)
		}
		return fmt.Errorf("创建文件 %s 失败: %w", path, err)
	}
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("写入文件 %s 失败: %w", path, werr)
	}
	if cerr != nil {
		_ = os.Remove(path)
		return fmt.Errorf("关闭文件 %s 失败: %w", path, cerr)
	}
	return nil
}

func sign(keyPath, inPath, outPath string) error {
	st, err := os.Stat(keyPath)
	if err != nil {
		return fmt.Errorf("读取私钥文件失败: %w", err)
	}
	if !st.Mode().IsRegular() {
		return errors.New("私钥路径不是普通文件")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return errors.New("私钥文件权限太宽，应为 0600")
	}

	priv, err := readPrivateKeyFile(keyPath)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("读取清单失败: %w", err)
	}
	if len(raw) > maxManifestBytes {
		return fmt.Errorf("清单超过最大大小（%d 字节）", maxManifestBytes)
	}
	if err := validateManifest(raw); err != nil {
		return err
	}

	sig := ed25519.Sign(priv, raw)
	pub := priv.Public().(ed25519.PublicKey)
	if !ed25519.Verify(pub, raw, sig) {
		return errors.New("签名自检失败")
	}

	out := sigJSON{
		KeyID: keyID(pub),
		Sig:   base64.StdEncoding.EncodeToString(sig),
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return fmt.Errorf("序列化签名文件失败: %w", err)
	}
	encoded = append(encoded, '\n')

	tmp := outPath + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return fmt.Errorf("写入临时签名文件失败: %w", err)
	}
	if err := os.Rename(tmp, outPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("写入签名文件失败: %w", err)
	}
	return nil
}

// verify 按节点端同一套规则核对：签名对得上 ring 里的某把公钥，清单本身合规。
// 发版时签完立刻跑一遍，私钥与仓库里的公钥对不上会在这里失败，而不是发出去之后所有节点都拒绝升级。
func verify(inPath, sigPath string, ring *upgrade.Keyring, stdout io.Writer) error {
	raw, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("读取清单失败: %w", err)
	}
	sig, err := os.ReadFile(sigPath)
	if err != nil {
		return fmt.Errorf("读取签名文件失败: %w", err)
	}
	if err := ring.Verify(raw, sig); err != nil {
		return fmt.Errorf("签名核对不过（签名私钥与 pdnd/upgrade/keys 里的公钥对不上？）: %w", err)
	}
	if _, err := upgrade.ParseManifest(raw); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "签名有效（内置公钥 %d 把）\n", ring.Len())
	return nil
}

func readPrivateKeyFile(path string) (ed25519.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥文件失败: %w", err)
	}
	trimmed := strings.TrimSpace(string(data))
	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, errors.New("私钥 base64 解码失败")
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("私钥长度错误: 期望 %d 字节，实际 %d 字节", ed25519.PrivateKeySize, len(decoded))
	}
	// 64 字节私钥 = 种子 + 公钥；后半截坏了时 Sign 照样出签名、自检也过（用的是同一个坏公钥），
	// 节点拿内置的真公钥却验不过。按种子重算一遍，对不上就拒绝。
	priv := ed25519.PrivateKey(decoded)
	if !priv.Equal(ed25519.NewKeyFromSeed(priv.Seed())) {
		return nil, errors.New("私钥文件损坏：种子与公钥不一致")
	}
	return priv, nil
}

func validateManifest(raw []byte) error {
	var hdr manifestHeader
	if err := json.Unmarshal(raw, &hdr); err != nil {
		return fmt.Errorf("清单不是合法的 JSON: %w", err)
	}
	if hdr.Schema != 1 {
		return fmt.Errorf("清单 schema 必须为 1，实际为 %d", hdr.Schema)
	}
	if hdr.Serial == 0 {
		return errors.New("清单 serial 必须大于 0")
	}
	if len(hdr.Artifacts) == 0 {
		return errors.New("清单 artifacts 不能为空")
	}
	return nil
}

func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:16]
}
