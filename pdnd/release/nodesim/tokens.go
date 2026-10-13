package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// ServerToken 是令牌文件里的一行：一台模拟服务器的名字与一次性接入令牌。
type ServerToken struct {
	Name  string
	Token string
}

// validName 判断名字是否合法：1–64 个 [A-Za-z0-9._-]。
func validName(s string) bool {
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

// ParseTokens 解析令牌文件：每行 `名字 令牌`（空白分隔，恰好两个字段）。
// 空行、首个非空白字符是 # 的行跳过；行尾 \r 去掉。名字只许 [A-Za-z0-9._-]，1–64 个字符（它也是身份目录名）。
// 名字重复、字段数不是 2、名字不合规都报错；错误信息只写行号与原因，绝不包含令牌。一台都没有也报错。
func ParseTokens(r io.Reader) ([]ServerToken, error) {
	sc := bufio.NewScanner(r)
	var out []ServerToken
	seen := make(map[string]int)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("第 %d 行：字段数应为 2，实际为 %d", lineNo, len(fields))
		}
		name, token := fields[0], fields[1]
		if !validName(name) {
			return nil, fmt.Errorf("第 %d 行：名字不合规", lineNo)
		}
		if prev, ok := seen[name]; ok {
			return nil, fmt.Errorf("第 %d 行：名字与第 %d 行重复", lineNo, prev)
		}
		seen[name] = lineNo
		out = append(out, ServerToken{Name: name, Token: token})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未解析到任何服务器令牌")
	}
	return out, nil
}

// LoadTokens 打开 path 调 ParseTokens（错误里带上 path，不带内容）。
func LoadTokens(path string) ([]ServerToken, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()
	tokens, err := ParseTokens(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return tokens, nil
}
