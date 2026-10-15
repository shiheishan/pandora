// depguardgen 按 internal/module/ 下的模块目录生成 .golangci.yml 里的 depguard 分层规则。
//
// 用法（在 panel/ 下）：
//
//	go run ./tools/depguardgen          重写 .golangci.yml
//	go run ./tools/depguardgen -check   只比较，有差异时退出码 1（CI 用）
//
// depguard 的 deny 只认导入路径前缀、不支持通配，所以「只许 import 本模块的 repo」
// 要为每个模块逐个列出别的模块，手写容易漏，由本工具生成。
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

const (
	modulePath = "github.com/aegispanel/aegis"
	configFile = ".golangci.yml"
)

func main() {
	check := flag.Bool("check", false, "只比较生成结果与现有文件")
	flag.Parse()

	mods, err := listModules("internal/module")
	if err != nil {
		fail(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data{Module: modulePath, Modules: mods}); err != nil {
		fail(err)
	}
	if *check {
		cur, err := os.ReadFile(configFile)
		if err != nil {
			fail(err)
		}
		if !bytes.Equal(cur, buf.Bytes()) {
			fmt.Fprintln(os.Stderr, ".golangci.yml 与模块目录不一致，在 panel/ 下运行 go run ./tools/depguardgen 重新生成")
			os.Exit(1)
		}
		return
	}
	if err := os.WriteFile(configFile, buf.Bytes(), 0o644); err != nil {
		fail(err)
	}
}

type module struct {
	Name   string
	Others []string
}

type data struct {
	Module  string
	Modules []module
}

func listModules(dir string) ([]module, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			names = append(names, filepath.Base(e.Name()))
		}
	}
	sort.Strings(names)
	mods := make([]module, 0, len(names))
	for _, n := range names {
		m := module{Name: n}
		for _, o := range names {
			if o != n {
				m.Others = append(m.Others, o)
			}
		}
		mods = append(mods, m)
	}
	return mods, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "depguardgen:", err)
	os.Exit(2)
}

var tmpl = template.Must(template.New("golangci").Parse(`# 由 tools/depguardgen 生成，不要手改；改规则改生成器，加模块后重新运行。
# tools、tests、web 不进 lint。
version: "2"

run:
  timeout: 5m

linters:
  default: none
  enable:
    - bodyclose
    - depguard
    - errcheck
    - gosec
    - govet
    - ineffassign
    - sqlclosecheck
    - staticcheck
    - unused
  settings:
    depguard:
      rules:
        handler-no-db:
          files:
            - "**/internal/module/*/handler/**"
            - "**/internal/module/*/task/**"
            - "**/internal/module/*/grpc/**"
          deny:
            - pkg: gorm.io
              desc: handler、task 与 grpc 不碰数据库，经 service 访问
            - pkg: {{.Module}}/internal/pkg/db$
              desc: handler 与 task 不碰数据库，经 service 访问
            - pkg: {{.Module}}/internal/pkg/tx
              desc: 事务边界在 service
        service-no-gorm:
          files:
            - "**/internal/module/*/service/**"
          deny:
            - pkg: gorm.io
              desc: service 不直接用 GORM，读写经本模块 repo，事务经 internal/pkg/tx
            - pkg: {{.Module}}/internal/pkg/db$
              desc: service 不直接取连接，事务经 internal/pkg/tx
        model-plain:
          files:
            - "**/internal/module/*/model/**"
          deny:
            - pkg: {{.Module}}/internal/module
              desc: model 只放字段与标签，不依赖任何模块
        pure-packages:
          files:
            - "**/internal/module/*/render/**"
            - "**/internal/module/*/protocol/**"
          deny:
            - pkg: {{.Module}}/internal/module
              desc: 纯函数包（订阅渲染、节点协议），不依赖任何模块
            - pkg: gorm.io
              desc: 纯函数包不碰数据库
            - pkg: {{.Module}}/internal/pkg/db$
              desc: 纯函数包不碰数据库
            - pkg: {{.Module}}/internal/pkg/tx
              desc: 纯函数包不碰数据库
        pkg-no-business:
          files:
            - "**/internal/pkg/**"
          deny:
            - pkg: {{.Module}}/internal/module
              desc: 基础设施不依赖业务模块
            - pkg: {{.Module}}/internal/app
              desc: 基础设施不依赖装配层
        httpx-no-db:
          files:
            - "**/internal/pkg/httpx/**"
            - "**/internal/pkg/ratelimit/**"
          deny:
            - pkg: gorm.io
              desc: 中间件不碰数据库
            - pkg: {{.Module}}/internal/pkg/db$
              desc: 中间件不碰数据库
        stack-conventions:
          files:
            - "**/internal/pkg/**"
            - "**/internal/app/**"
            - "**/internal/module/**"
            - "**/cmd/pandora/**"
            - "**/db/migrations/**"
          deny:
            - pkg: github.com/go-chi/chi
              desc: 新栈用 gin
            - pkg: github.com/jackc/pgx/v5/pgxpool
              desc: 新栈经 GORM 访问数据库
        # 标准库日志只在生产代码里禁：测试里第三方夹具的 API 可能要求传 *log.Logger
        stack-no-stdlog:
          files:
            - "**/internal/pkg/**"
            - "**/internal/app/**"
            - "**/internal/module/**"
            - "**/cmd/pandora/**"
            - "**/db/migrations/**"
            - "!$test"
          deny:
            - pkg: log$
              desc: 日志用 zap（internal/pkg/log）
            - pkg: log/slog
              desc: 日志用 zap（internal/pkg/log）
        # 只有用 database/sql 打开连接的地方（goose 迁移、测试夹具）可以碰 database/sql；测试照旧可用
        stack-no-database-sql:
          files:
            - "**/internal/pkg/**"
            - "**/internal/app/**"
            - "**/internal/module/**"
            - "**/cmd/pandora/**"
            - "!$test"
            - "!**/internal/app/migrate.go"
            - "!**/internal/pkg/testkit/**"
          deny:
            - pkg: database/sql
              desc: 经 GORM 访问数据库（internal/pkg/db），取不到行看 RowsAffected 或 gorm.ErrRecordNotFound
        # pgx 只在 internal/pkg/db（约束违例翻译）与用 pgx 驱动打开 database/sql 的地方出现；测试同样适用
        stack-no-pgx:
          files:
            - "**/internal/pkg/**"
            - "**/internal/app/**"
            - "**/internal/module/**"
            - "**/cmd/pandora/**"
            - "**/db/migrations/**"
            - "!**/internal/pkg/db/**"
            - "!**/internal/pkg/testkit/**"
            - "!**/internal/app/migrate.go"
            - "!**/db/migrations/migrations_test.go"
          deny:
            - pkg: github.com/jackc/pgx
              desc: 约束违例经 db.AsViolation / db.ConstraintErrors 翻译，可重试错误经 db.IsRetryable 判断
{{- range .Modules}}
{{- $m := .Name}}
{{- if .Others}}
        # handler、task、grpc 经本模块 service 读写，不直接用 repo，也不把 model 直接序列化出去
        module-{{$m}}-entry:
          files:
            - "**/internal/module/{{$m}}/handler/**"
            - "**/internal/module/{{$m}}/task/**"
            - "**/internal/module/{{$m}}/grpc/**"
          deny:
            - pkg: {{$.Module}}/internal/module/{{$m}}/repo
              desc: 经本模块 service 读写
            - pkg: {{$.Module}}/internal/module/{{$m}}/model
              desc: 对外用 service 的类型与 handler 的 DTO，不直接用 model
        module-{{$m}}-boundary:
          files:
            - "**/internal/module/{{$m}}/**"
            - "!$test"
          deny:
{{- range .Others}}
            - pkg: {{$.Module}}/internal/module/{{.}}/repo
              desc: 别的模块的数据经它的 service 取
            - pkg: {{$.Module}}/internal/module/{{.}}/model
              desc: 别的模块的数据经它的 service 取
            - pkg: {{$.Module}}/internal/module/{{.}}/handler
              desc: 模块之间只调 service
            - pkg: {{$.Module}}/internal/module/{{.}}/task
              desc: 模块之间只调 service
            - pkg: {{$.Module}}/internal/module/{{.}}/grpc
              desc: 模块之间只调 service
            - pkg: {{$.Module}}/internal/module/{{.}}/provider
              desc: 模块之间只调 service
{{- end}}
        # 测试可以用别的模块的 repo 装出真的 service（和 app 装配一样），也可以用支付渠道 provider 造验签回调；不碰它的 model、handler、task、grpc
        module-{{$m}}-boundary-test:
          files:
            - "**/internal/module/{{$m}}/**/*_test.go"
          deny:
{{- range .Others}}
            - pkg: {{$.Module}}/internal/module/{{.}}/model
              desc: 别的模块的数据经它的 service 取
            - pkg: {{$.Module}}/internal/module/{{.}}/handler
              desc: 模块之间只调 service
            - pkg: {{$.Module}}/internal/module/{{.}}/task
              desc: 模块之间只调 service
            - pkg: {{$.Module}}/internal/module/{{.}}/grpc
              desc: 模块之间只调 service
{{- end}}
{{- end}}
{{- end}}
    gosec:
      excludes:
        # G104 与 errcheck 重复
        - G104
  exclusions:
    # 社区通用预设：不强制检查写终端、Close、Unsetenv 一类的错误
    presets:
      - std-error-handling
    paths:
      - tools
      - tests
      - web
    rules:
      - path: _test\.go
        linters:
          - gosec
`))
