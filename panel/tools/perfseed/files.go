package main

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const maxPasswordLen = 256

// readPasswordFile 读口令文件：权限须 0600，去掉末尾换行，不含内容泄露。
func readPasswordFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("读取口令文件失败: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("口令文件权限过宽，须禁止组和其他用户访问: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取口令文件失败: %w", err)
	}
	if len(data) == 0 {
		return "", fmt.Errorf("口令文件不能为空: %s", path)
	}
	pwd := string(data)
	if strings.HasSuffix(pwd, "\r\n") {
		pwd = pwd[:len(pwd)-2]
	} else if strings.HasSuffix(pwd, "\n") {
		pwd = pwd[:len(pwd)-1]
	}
	if pwd == "" {
		return "", fmt.Errorf("口令文件不能为空: %s", path)
	}
	if strings.Contains(pwd, "\n") || strings.Contains(pwd, "\r") {
		return "", fmt.Errorf("口令文件须为单行: %s", path)
	}
	if len(pwd) > maxPasswordLen {
		return "", fmt.Errorf("口令长度不能超过 %d 字节: %s", maxPasswordLen, path)
	}
	return pwd, nil
}

// readDSNFile 读 DSN 文件或 deploy.env 风格的一行。
func readDSNFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 DSN 文件失败: %w", err)
	}
	content := string(data)
	const prefix = "PANDORA_DATABASE_MIGRATION_URL="
	var dsn string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, prefix) {
			dsn = strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	if dsn == "" {
		dsn = strings.TrimSpace(content)
		if strings.Contains(dsn, "\n") || strings.Contains(dsn, "\r") {
			return "", fmt.Errorf("DSN 须为单行: %s", path)
		}
	}
	if dsn == "" {
		return "", fmt.Errorf("DSN 为空: %s", path)
	}
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return "", fmt.Errorf("DSN 须以 postgres:// 或 postgresql:// 开头: %s", path)
	}
	return dsn, nil
}

// readEnvValue 从 KEY=VALUE 文件读取键（最后一次出现为准）。
func readEnvValue(path, key string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("读取环境文件失败: %w", err)
	}
	defer f.Close()
	var value string
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx < 0 {
			continue
		}
		k := trimmed[:idx]
		if k == key {
			value = trimmed[idx+1:]
			found = true
		}
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("读取环境文件失败: %w", err)
	}
	if !found || value == "" {
		return "", fmt.Errorf("环境文件 %s 中缺少键 %s 或值为空", path, key)
	}
	return value, nil
}

// writeFileAtomic 原子写入，权限 0600。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("设置临时文件权限失败: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("替换目标文件失败: %w", err)
	}
	cleanup = false
	return nil
}

// saveJSON 格式化 JSON 并原子写入。
func saveJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return writeFileAtomic(path, b)
}

// loadJSON 解码 JSON；不存在返回 (false, nil)。
func loadJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return true, err
	}
	return true, nil
}

// fileExists 判断路径是否存在。
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// TokenEntry 是令牌文件的一行。
type TokenEntry struct {
	Name  string
	Token string
}

func validTokenName(s string) bool {
	if len(s) < 1 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

// formatTokens 生成 tokens 文件内容。
func formatTokens(entries []TokenEntry) ([]byte, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("令牌列表不能为空")
	}
	seen := make(map[string]struct{})
	var buf strings.Builder
	for i, e := range entries {
		if !validTokenName(e.Name) {
			return nil, fmt.Errorf("第 %d 条：名字不合规", i+1)
		}
		if _, ok := seen[e.Name]; ok {
			return nil, fmt.Errorf("第 %d 条：名字重复", i+1)
		}
		seen[e.Name] = struct{}{}
		if e.Token == "" {
			return nil, fmt.Errorf("第 %d 条：令牌不能为空", i+1)
		}
		if strings.IndexFunc(e.Token, unicode.IsSpace) >= 0 {
			return nil, fmt.Errorf("第 %d 条：令牌不能含空白", i+1)
		}
		buf.WriteString(e.Name)
		buf.WriteByte(' ')
		buf.WriteString(e.Token)
		buf.WriteByte('\n')
	}
	return []byte(buf.String()), nil
}

var (
	rePanel    = regexp.MustCompile(`--panel\s+'([^']+)'`)
	reCASha256 = regexp.MustCompile(`--ca-sha256\s+'([^']+)'`)
)

// parseInstallCommand 从安装命令提取 panel 与 CA 指纹。
func parseInstallCommand(cmd string) (panel, caSHA256 string, err error) {
	mPanel := rePanel.FindStringSubmatch(cmd)
	if len(mPanel) < 2 {
		return "", "", fmt.Errorf("安装命令缺少 --panel 参数")
	}
	mCA := reCASha256.FindStringSubmatch(cmd)
	if len(mCA) < 2 {
		return "", "", fmt.Errorf("安装命令缺少 --ca-sha256 参数")
	}
	panel = mPanel[1]
	caSHA256 = mCA[1]
	if !strings.HasPrefix(caSHA256, "sha256:") {
		return "", "", fmt.Errorf("--ca-sha256 须以 sha256: 开头")
	}
	return panel, caSHA256, nil
}

// formatNodesimEnv 写 nodesim.env 内容。
func formatNodesimEnv(panel, caSHA256 string) []byte {
	return []byte(fmt.Sprintf("PANEL=%s\nCA_SHA256=%s\n", panel, caSHA256))
}

// parseNodesimEnv 解析 nodesim.env。
func parseNodesimEnv(data []byte) (panel, caSHA256 string, err error) {
	var gotPanel, gotCA bool
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		idx := strings.Index(trimmed, "=")
		if idx < 0 {
			continue
		}
		k := trimmed[:idx]
		v := trimmed[idx+1:]
		switch k {
		case "PANEL":
			panel = v
			gotPanel = true
		case "CA_SHA256":
			caSHA256 = v
			gotCA = true
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", err
	}
	if !gotPanel {
		return "", "", fmt.Errorf("nodesim.env 缺少 PANEL")
	}
	if !gotCA {
		return "", "", fmt.Errorf("nodesim.env 缺少 CA_SHA256")
	}
	return panel, caSHA256, nil
}

// formatAdminEnv 写 admin.env 内容。
func formatAdminEnv(email string) []byte {
	return []byte(fmt.Sprintf("ADMIN_EMAIL=%s\n", email))
}

// writeUsersCSV 写出 users.csv。
func writeUsersCSV(w io.Writer, password string, rows []CSVRow) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"user_id", "email", "password", "sub_token"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := cw.Write([]string{r.UserID, r.Email, password, r.SubToken}); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// serverName 返回服务器名 s0001 形式。
func serverName(i int) string {
	return fmt.Sprintf("s%04d", i)
}

// userEmail 返回 perf 用户邮箱。
func userEmail(i int) string {
	return userEmailPrefix + fmt.Sprintf("%05d", i) + userEmailDomain
}
