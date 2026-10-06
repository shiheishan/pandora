---
paths:
  - "panel/internal/domain/dbbackup/**"
---

# 数据库备份离机与保留

- 所有私密输入（WebDAV 口令、清单签名种子、检查点、复制 Hook）只能经 `openSecureRegular` / `validateSecureParent` 读入：属主必须是 `secureFileOwnerUID`（生产恒为 root）、组与其他人没有任何权限位、单硬链接、父目录同样受保护且路径不经符号链接；先查父目录、再查已打开的 fd，防 TOCTOU。新增私密输入不要另开 os.Open
- 这组校验只在 Linux 生效（`*_linux.go`），其他平台是空实现（`*_other.go`）。在 macOS 上测试通过不代表 Linux 行为正确，Linux 行为只能在 Linux 上验证；CI panel-unit 以非 root 跑，测试经 `secureTempDir` 和改写 `secureFileOwnerUID` 把当前用户设为可信属主
- `secureFileOwnerUID` 与 `checkpointHookRoot` 是包级变量只为测试改写，不得经配置、环境变量或导出接口暴露；Hook 路径固定在 `checkpointHookRoot`，不可配置到别处（能配路径就等于能执行任意代码）
- 删除远端旧备份时清单先行（`OrderedObjects`），崩溃也不会留下指向缺失数据的清单；每次至多执行一次远端删除，远端最多领先持久意图一步。删除权来自持久化、已校验的删除意图，意图存储与远端接口都封在包内，外部不能按名删除
- `PlanRetention` 遇到任何不完整、未校验或有歧义的清单都返回空计划和错误，调用方不得在出错时执行删除
