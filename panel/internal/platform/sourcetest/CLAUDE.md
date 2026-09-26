# panel/internal/platform/sourcetest/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

源码契约测试的读源码入口：按「包 + 顶层声明名」取原文，不按文件名读。这类测试断言「某函数里必须有 / 不许有某段代码」，过去按文件名读、用「函数 A 到函数 B」的文本窗口截函数体，函数一换文件就红，否定断言更会在目标挪走后静默通过（phase5-refactor.md ①）。这里名字找不到或重名一律失败，不给静默留余地；否定断言用 Source 覆盖整个包，函数挪到哪个文件都逃不掉。只被 *_test.go 引用，不进生产二进制，同 pg18test。

成员清单
sourcetest.go: Load 解析目录下全部非测试 .go（不看构建约束）；Decl / Decls 按名取声明原文（方法写 接收者.方法，不含文档注释、保留原排版），DeclWithDoc 连同文档注释（断言落在注释里的契约用），FuncDecl 给走 AST 的断言，Source 给整包否定断言
testdata/fixture/: 自测用的假包：泛型接收者、分组常量、按构建约束分成两份的同名函数（验证重名即失败）、一个 _test.go（验证不计入）
*_test.go: 用会 panic 的假 TB 断言查找失败确实让测试失败

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
