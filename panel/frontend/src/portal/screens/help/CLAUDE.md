# panel/frontend/src/portal/screens/help/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

帮助中心（用户门户-09-帮助.dc.html；契约门户-09，修订 R43）。地址驱动：#/help 宽屏左列目录、右列目录第一篇（搜索时即第一条命中），#/help/<slug> 打开指定文章；< 640 目录与文章分屏，文章卡顶部「全部文章」返回。
列表一次取全（后端上限 200、不带正文），只留 kb_article / tutorial，按 category 出现顺序分组、空分类归「其他」排最后；搜索框 300 毫秒防抖后交给后端 q（要匹配正文，而列表拿不到正文），换词时保留上一份结果不闪骨架。
可见性参数 platform=any 在列表、正文、反馈三处一致：后端按同一套规则判定，参数不一致反馈会 404；网页没有客户端版本，限定了客户端版本的文章不可见，这与后端行为一致。
正文是纯文本 / Markdown 源码，只认「## 」行作小标题、空行分段，全部渲染成文本节点，不用 innerHTML。「有帮助」每篇每版只发一次（按用户、文章、版本 upsert，天然幂等，不带键）；「仍未解决，提交工单」不调接口，直接去 #/tickets/new。设计稿只有「有帮助」，没有「没帮助」按钮，照设计。

成员清单
index.tsx: 页面组件——搜索框、分组目录（锚点，当前篇 aria-current）、文章（头部分类与更新日期、正文块、反馈条）、404「不存在或已下线」
api.ts: 数据层——文章 schema（omitempty 字段可选，空正文归一为 ''）、列表 / 正文查询、反馈 mutation，共用 HELP_QUERY
model.ts: 纯映射——helpGroups、articleBlocks、SEARCH_MAX
Help.module.css: 页面样式，取自设计稿门户-09
help.test.ts: 第 ⑥ 步帮助中心的单元测试（schema、分组、正文拆块）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
