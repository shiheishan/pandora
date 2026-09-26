// [INPUT]: 依赖 node_refusal.go 的 NodeStatusRefusal，依赖 pgconn.PgError 造数据库错误；扫描 panel/internal 非测试源码
// [OUTPUT]: 对外提供 TestNodeStatusRefusalTranslates、TestNoRawDatabaseMessageInHTTPErrors
// [POS]: nodefabric 的单元测试：节点状态报错按约束名译中文、触发器中文原样、英文原句只进日志；并守住全仓不再把 db.Message 直接塞进 httpx 错误（⑪）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestNodeStatusRefusalTranslates(t *testing.T) {
	cases := []struct {
		name, constraint, message, want string
	}{
		{"trigger", "", "节点 x 非法状态跳转：draft -> active（PRD 8.1 生命周期状态机）", "非法状态跳转：draft -> active"},
		{"english raise", "", "some english guard failed", "数据库拒绝了这次节点状态变更"},
		{"status", "nodes_status_check", `new row for relation "nodes" violates check constraint "nodes_status_check"`, "节点状态取值不合法"},
		{"serving", "nodes_serving_status_check", `violates check constraint "nodes_serving_status_check"`, "节点服务状态取值不合法"},
		{"desired pair", "nodes_desired_effective_pair_check", `violates check constraint "nodes_desired_effective_pair_check"`, "目标配置版本不完整"},
		{"unknown", "nodes_weight_check", `violates check constraint "nodes_weight_check"`, "节点数据不满足数据库约束"},
	}
	for _, tc := range cases {
		pgErr := &pgconn.PgError{Code: "23514", ConstraintName: tc.constraint, Message: tc.message}
		got := NodeStatusRefusal(pgErr)
		if got.Code != httpx.CodeConflict || !strings.Contains(got.Message, tc.want) {
			t.Errorf("%s: code=%s message=%q want 409 containing %q", tc.name, got.Code, got.Message, tc.want)
		}
		if tc.constraint != "" && strings.Contains(got.Message, tc.constraint) {
			t.Errorf("%s: constraint name leaked into the message %q", tc.name, got.Message)
		}
		// 原句留给日志
		var inner *pgconn.PgError
		if !errors.As(got, &inner) || inner != pgErr {
			t.Errorf("%s: database error not kept as the internal cause", tc.name)
		}
	}
}

var rawDBMessage = regexp.MustCompile(`httpx\.(New|Error)\b[^\n]*db\.Message\(`)

// db.Message 是数据库原句，只配进日志：约束报错是英文，还带表名与约束名（SEC-006）。
func TestNoRawDatabaseMessageInHTTPErrors(t *testing.T) {
	var offenders []string
	err := filepath.WalkDir(filepath.Join("..", ".."), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rawDBMessage.Match(body) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(offenders) > 0 {
		t.Fatalf("raw database messages returned to clients in %v; translate by constraint name (see NodeStatusRefusal)", offenders)
	}
}
