# 参与 ServerS5A3

公开源码以 [GitHub master](https://github.com/Cec1c/ServerS5A3/tree/master) 为准，修复和建议在本仓库提交。GitGud 是异步镜像。中文或 English 均可。

## 报告问题

在 [Issues](https://github.com/Cec1c/ServerS5A3/issues/new/choose) 选择错误报告或功能建议。问题描述优先写实际行为、预期结果和操作过程；版本不明确时填“未知”。偶发问题可附最后一次发生的时间与相关日志，不要求先找出根因。

已知的服务端提交、EXE/PVF 版本或 SHA256 有助于核对配套关系。日志与截图只保留相关片段，移除密码、令牌、玩家账号及服务器连接信息。登录器界面、自更新和独立 GM 功能转到 [PurpleMagic](https://github.com/Cec1c/PurpleMagic/issues)。

## 提交修复

1. 从最新 `master` 建 `fix/` 或相应功能分支，先读 [AGENTS.md](AGENTS.md) 与相关模块说明。
2. 聚焦问题，补能覆盖原失败场景的回归。相关小修复可在一个累计 PR 中保留各自提交，准备好后统一运行 CI；未完成时使用 Draft。
3. 按 PR 模板说明行为、范围、资源依赖与实际验证。无需为纯文档运行游戏全量测试，服务端验证和构建使用 Linux。
4. 添加一份变更记录，提交到本仓库 `master`。普通外部 PR 通过远端 CI 后合入。

### 变更记录示例

新增 `changelog.d/YYYYMMDD-事项.fix.md`；功能、流程、文档也可分别使用 `.feature.md`、`.change.md`、`.docs.md`。

```markdown
---
category: 功能修复
---

# 简短说明修复后的行为

## 变更

- 描述触发条件与修复结果。

## 验证

- 列实际命令或场景，以及通过、失败、跳过或待验收状态。
```

使用 UTF-8 / LF。已合并片段保留原文，纠正时新增记录。历史片段缺少章节会提示，新提交的片段仍需采用上述格式。

```text
python scripts/ci/changelog_fragments.py check --base origin/master
git diff --check
```

### CI 与本地验证

`Verify` 先检查改动范围和新增变更记录，再按路径执行 Linux 游戏检查、工具检查或容器构建。`scope` 失败时先查看它的日志；其后显示 `skipped` 表示未运行，不是游戏测试失败。首次外部贡献可能等待维护者批准运行 Actions。

管理员批准的两位直接写入开发者可按 [trusted quick](Docs/TRUSTED_QUICK_MERGE.md) 提交绑定精确 SHA 的本地验证声明；它记录为 `local-attested`，普通外部 PR 继续使用 CI。

具体服务端命令见 [AGENTS.md](AGENTS.md)。涉及资源时说明 EXE/PVF 配对、频道表及派生索引；源码合入、资源发布、部署和玩家实测分别记录。
