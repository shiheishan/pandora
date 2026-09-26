# panel/tools/refactorcheck/
> L2 | 父级: /panel/CLAUDE.md（tools/ 一行）

第 5 阶段超长文件重构（phase5-refactor.md）的机器自证工具：重构铁律是「只挪代码、不改行为」，这里把它变成两条可复跑的检查。main 包，只用 `go run` 在开发机与验收时跑，不被任何包 import，不进发布包（build-release.sh 只编 cmd/<名字>）。只依赖标准库与本机的 git、go。

用法（在 module 根目录 panel/ 下）
- 提交前自查：`go run ./tools/refactorcheck compare`（base 默认 HEAD，head 默认工作树含未跟踪文件，不给目录时比对全部有 .go 变化的目录）
- 验收某个提交：`go run ./tools/refactorcheck compare -base <sha>^ -head <sha>`；拆测试文件时加 `-tests`
- pdnd（另一个 module）：`go run ./tools/refactorcheck compare -C ../pdnd -base <sha>^ -head <sha>`
- 打散验证：`go run ./tools/refactorcheck shatter -out <新目录> [-skip '^internal/domain/billing$']`，再按输出提示在副本里跑 vet 与全量测试；③ 把 billing 的测试改成按声明名读之前，billing 要 -skip

成员清单
main.go: 子命令分发与用法说明
decls.go: 声明指纹：键为「包名 种类 名字」，值为所在文件构建约束的真值表（//go:build 与 _GOOS_GOARCH 后缀两种写法等价）+ 用到的导入路径 + gofmt 后原文（含文档注释与函数体内注释）；init、var _ 这类可重名的按多重集合比；不属于任何声明的游离注释（分节标题）单独收集，只提示不判错
compare.go: compare 子命令：从 git 版本或工作树读一个目录的源文件，两侧指纹多重集合完全相同即 OK，否则列出只在一侧的声明与指纹变了的声明并以状态 1 退出；输出两侧各文件行数，填报告里的新旧行数
shatter.go: shatter 子命令：按 git ls-files 复制仓库（跟踪与未跟踪、不含被忽略的），再把每个非测试文件的每个顶层声明拆进随机命名的文件，块内带上前置注释、原样的 //go:build 行、保留 _GOOS_GOARCH 后缀、按用法挑选导入；测试文件、-keep 匹配的文件（默认 api/admin 的 router*.go，路由契约按设计读它们）与只有导入的文件原样保留
*_test.go: 纯挪动判相同、改体 / 改文档注释 / 换约束 / 换导入路径 / 改名 / 丢一个 init 判不同；shatter 的输出经 compare 判为纯挪动

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
