---
paths:
  - "pdnd/certbundle/**"
  - "pdnd/certstore/**"
  - "pdnd/bindingcontract/**"
  - "panel/internal/platform/certbundle/**"
  - "panel/internal/platform/bindingcontract/**"
  - "docs/server-binding-contract.md"
  - "docs/node-certificates.md"
---

# 面板与 pdnd 两端共用的契约包

panel 与 pdnd 是两个独立的 Go module，下面两组包是同一份契约在两端各写一遍，靠同一份金样本对齐：

| 契约 | 面板 | pdnd | 金样本（两份必须逐字节相同） | 文档 |
|---|---|---|---|---|
| 证书包（HPKE 加密、签名、原像） | `panel/internal/platform/certbundle` | `pdnd/certbundle`（`pdnd/certstore` 消费解开后的证书） | 各自的 `testdata/certbundle-v1-golden.json` | `docs/node-certificates.md` |
| 服务器绑定（请求签名、清单、名单、钉住、升级、枚举） | `panel/internal/platform/bindingcontract` | `pdnd/bindingcontract` | 各自的 `testdata/bindingcontract-v1-golden.json` | `docs/server-binding-contract.md` |

- 改一端的格式、原像、枚举、失败码，必须同改另一端和文档，再重跑两端的金样本测试。只改一端时两边的测试都可能是绿的（各自对着旧金样本），坏在线上。
- 金样本只在面板侧生成，一次写两份（面板和 pdnd 的 `testdata/`），两份一起提交：
  - 证书包：在 `panel/` 下 `go test ./internal/platform/certbundle -run TestGolden -certbundle.update`。HPKE 每次封装都随机，所以只在改契约时重生成。
  - 绑定：在 `panel/` 下 `go test ./internal/platform/bindingcontract -run TestGoldenFileMatchesDefinitions -bindingcontract.update`。
- 两端的验证命令（改完都要跑）：
  - `cd panel && go test -count=1 ./internal/platform/certbundle ./internal/platform/bindingcontract`
  - `cd pdnd && go test -count=1 ./certbundle ./certstore ./bindingcontract`
- 守卫：面板侧 `TestGoldenVectorsIdenticalInPanelAndPdnd`（certbundle）、`TestGoldenIdenticalInPanelAndPdnd`（bindingcontract），pdnd 侧 `TestGoldenIdenticalInPdndAndPanel`（bindingcontract）比对两份金样本；两端各自的 `TestGoldenVectorsMatch*Implementation`、`TestGolden*` 用金样本验本端实现。
- 枚举（失败码、状态、事件类型等）两端常量要与金样本的 `enums` 逐项、同序一致（`docs/server-binding-contract.md` 第 10、17 节）。
