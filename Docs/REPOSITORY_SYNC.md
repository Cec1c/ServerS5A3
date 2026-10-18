# GitGud 权威源码与 GitHub 单向镜像

GitGud 上游主干是唯一权威源码。独立功能、修复、测试、文档和 CI 分支均通过 MR 汇入上游；GitHub 只镜像上游的确切提交，不作为另一条开发主干。GitGud fork 仅用于承载贡献分支。

## Agent 工作流

1. 拉取实际上游主干，核对已有 MR；同一事项更新原 MR，不重复创建。
2. 在干净的独立工作树审查精确差异。将分支推到 GitGud fork，创建面向上游 master 的中文 MR，说明行为、验证、依赖和待实机验收项。已提交并准备集成的分支不能仅推到 fork 就视为完成贡献。
3. 出现冲突时由负责同步的 Agent 合入最新上游，逐项保留双方有效逻辑，验证后非强推更新原分支。不得用整文件覆盖、强推或丢弃既有修复来消除冲突。
4. 按用户已有授权和项目评审要求合并 MR，核对最终上游 SHA。草稿、研究与未验证候选在 MR 中明确状态，不因成功推送而称已合并。
5. GitHub Actions 单向快进至上游相同 SHA。读回上游与 GitHub 主干；不在 GitHub 创建同步 PR、不产生镜像合并提交、不把 GitHub 独有改动回推上游。
6. 部署从实际 active 累计基线保留已上线功能与私有扩展，独立完成资源门槛、测试、备份、切换及运行读回。源码 MR、上游合并、镜像成功和游戏部署分别记录。

CI 与同步脚本的变更同样在上游 MR 审查，合并后正常镜像。镜像进程只执行当前已检出的脚本，不执行本次抓取的代码。

## GitHub 配置

- 变量 GITGUD_UPSTREAM_REPOSITORY：唯一上游的公开 HTTPS Git URL，例如 https://gitgud.io/OWNER/PROJECT.git；未设置时禁用镜像。
- 在目标 GitHub 仓库创建专用、允许写入的 Deploy Key。私钥存入仓库 secret SOURCE_MIRROR_SSH_KEY，已核实的 github.com 主机密钥存入 SOURCE_MIRROR_KNOWN_HOSTS。该身份只写本 GitHub 镜像，不使用个人长期访问令牌或 GitGud 写入凭据。
- Actions 默认 token 仅 contents: read。主干保护需允许专用镜像身份按项目规则快进；不要在 GitHub 主干直接开发或合并 PR。
- Repository sync 在 master push、手动触发及每15分钟计划运行。调度可能延迟。GitHub Deploy Key 推送可能产生一次后续检查；相同SHA无写入，不会持续产生新提交。
- 旧 Upstream updates 工作流及其合并脚本已移除。迁移完成后停用旧工作流，移除旧 GITGUD_SYNC_REPOSITORY、UPSTREAM_SYNC_ONLY 变量和 GitGud fork 同步 secrets，避免双向写入重新启用。

| 主干关系 | 动作 |
| --- | --- |
| 相同 SHA | 不写入 |
| GitHub 是上游的祖先 | GitHub 非强推快进到上游确切 SHA |
| GitHub 独自领先或双方分叉 | 停止；Agent 通过上游 MR 归并合法改动，然后继续快进 |
| 抓取后任一引用变化 | 重新读取再执行；不覆盖中途提交 |

不强推、不删除分支、不移动标签、不在镜像端合并。源码镜像不修改 Release、Issue、MR、设置、密钥、玩家资源或线上服务。客户端 EXE Release 仍按独立批准的流程执行，见 Docs/CLIENT_RELEASE_SYNC.md；本次源码权威迁移不调整其资产规则。

## 从多主干迁移

先保存两端精确 SHA，并暂停旧双向同步与 GitHub 自动合并工作流。审查 GitHub 独有提交，通过上游 MR 保留需继承的历史与功能。上游合并后必须成为现有 GitHub 主干的后继，再以非强推引导 GitHub 到该上游提交；不 reset 或覆盖历史。配置专用镜像凭据，启用单向工作流并验证一次真实运行及相同版本无写入。

没有 Runner 的贡献 fork 可关闭自身 CI/CD；不因此关闭原作者 CI。Verify 保持手动入口，源码镜像不自动构建或部署游戏。

## 本地验证

```text
python -m unittest discover -s scripts/ci -p "test_*.py" -v
python scripts/ci/sync_repositories.py --github <GitHub镜像URL> --upstream <GitGud权威URL>
```

默认 dry run；--apply 仅写 GitHub。回归覆盖相等、快进、GitHub领先、分叉、上游移动、并发写入拒绝、CI文件同步与抓取脚本不执行。输出不包含凭据或提供者原始错误。
