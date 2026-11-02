# GitHub 权威源码与 Linux CI：开发机迁移交接

2026-11-02 生效。将本文件交给其他开发者的 Agent，要求修改它实际使用的 AGENTS 与发布说明；只读本文件不算完成迁移。

## 新入口

- 公开源码唯一权威：[Cec1c/ServerS5A3 master](https://github.com/Cec1c/ServerS5A3)。
- 新贡献统一到 [GitHub Pull requests](https://github.com/Cec1c/ServerS5A3/pulls)，功能修复用 `fix/` 分支。GitGud 不再接收新的功能 MR。
- [GitGud 16ww/servers5a3](https://gitgud.io/16ww/servers5a3) 是异步源码镜像，只从 GitHub 非强推快进。镜像延迟、失败、自建 Runner 排队均不阻塞 GitHub PR、下游同步或部署。
- 群服继续使用自己的私有累计仓库 master 作为部署权威；保留群服内容。公开 GitHub 不接收私有累计历史、配置、取证和凭据。
- 所有分支名继续使用 `master`。

## 给 Agent 的替换规则

把以下内容合入工作区、源码、构建和部署目录中实际生效的 AGENTS；替换相矛盾的旧规则，保留其他任务约束。

> 公开源码以 GitHub Cec1c/ServerS5A3 master 为唯一权威，通用功能、修复、测试、文档和 CI 通过 GitHub PR 集成。GitGud 仅作异步单向镜像，不再作为 PR、下游同步或发布的前置条件。私有群服累计仓库仍是群服部署唯一权威，群服内容走私有 group/ PR，不能向公开仓库推送私有历史。
>
> 服务端只验证和构建 Linux。禁用 Windows CI job、Windows 交叉构建及部署前 Windows 构建；Windows 开发机上的 BAT/Python 操作入口仍可使用。不得为了旧 Windows 门禁阻塞 Linux 发布。
>
> 公开 PR 使用 GitHub 托管 Linux Runner，按实际 diff 选择玩法、部署工具、容器或文档检查。同一 PR 新提交取消过时纯验证；普通分支 push 和合并后的 push 不重复整套验证。发布/同步/切换任务不自动取消，镜像不生成六套构建附件。
>
> 源码测试在贡献阶段完成。部署 prepare 不再次执行完整 Go test/vet、Windows 构建或真实 PVF 专项，只校验依赖并从当次成功 pull 的最新私有权威源码重新构建 Linux 正式服务端。回执明确 testsExecuted=false，不称为复用了 CI 证据。缺陷修复需要的专项在提交前做，新增故障或输入变化时按范围补查。
>
> 保留私有主干 pull、累计范围及资源身份核对、备份/回退、准备摘要、当次维护批准、串行切换和实际程序/健康读回。纯工具提交不额外重启游戏；有有效任务先接管原 request ID，不换入口重放。
>
> 变更记录仍每项独立 changelog.d 文件；不回写历史 changlog.md。工作树保留未提交修改；完成归档且无发布依赖后回收临时树。

## 开发机操作

先执行 `git status --short`、`git remote -v`，保留脏文件和原分支。检查当前 origin 的用途，不能把私有 origin 改为公开库。

对于公开源码工作区，如果原 origin 是个人 fork，保留 fork，并添加或更新单独的权威 remote：

```text
git remote add authority https://github.com/Cec1c/ServerS5A3.git
git fetch authority master
git worktree add ../public-fix -b fix/<事项> authority/master
```

已有 authority 时先核对，使用 `git remote set-url authority <上述地址>` 更新。可推送个人 GitHub fork 后向权威仓库提 PR，也可按仓库权限推送贡献分支。同一事项继续原 GitHub PR，不重复开单。

GitGud 现有未合 MR 保留历史和作者贡献：读取原 MR、源分支及精确 diff，与 GitHub 最新 master 比较，迁移尚未包含的改动为 GitHub PR，关联原 MR。不要整份复制旧树，不自动关闭含独立内容的 MR，不强推覆盖历史。

## CI 和下游

公开 `Verify` 在 PR 或手动触发；文档不跑 Go，工具改动运行 Python/脚本回归，玩法变动运行领域/桥接检查和 Linux server 构建，Dockerfile 变化检查容器。必要 Linux 控制工具仍受源码编译/test/vet 覆盖，不再生成其常规发布附件。合并 gate 为 `required`，不依赖 GitGud 状态。

下游 `Public to PoS5A3` 直接读取公开 GitHub master。需要立即累计时手动触发，`required_commit` 填实际 GitHub 合并的完整 SHA。普通源码自动合并；涉及受保护 CI/同步策略或真实冲突时走私有 PR 人工审查，保留双方逻辑。

群服发布继续使用私有 Actions 的统一 Production deployment 入口及发布机队列。其他开发机不复制生产 SSH 私钥，不创建第二个发布 worker。源码 PR 合并、私有累计、prepare、activate、玩家验收分别记录。

## 镜像管理员

`Repository sync` 从 GitHub 推 GitGud master，GitHub Actions 使用单独 GitGud 写入 secret `GITGUD_SOURCE_MIRROR_TOKEN` 与变量 `GITGUD_MIRROR_REPOSITORY`；凭据只供受信 master 镜像 job，不供 PR。仅允许非强推：相同 SHA 无操作；GitGud 领先/分叉或执行中主干变化则停止，保留现场并通过 GitHub PR 集成合法贡献。

GitGud 自动 push/MR 流水线已禁用，只保留可手动运行的镜像说明。旧 GitGud → GitHub 源码同步必须停用。客户端 Release 资产同步是独立流程，此次不重发资源，也不把源码镜像变成游戏发布门槛。

## 开发机完成回执

记录实际工作区和生效 AGENTS 路径、修改前后 Git remotes、公开/私有主干 SHA、迁移中的旧 MR 与新 PR 链接。确认所有现行说明不再要求“先合 GitGud MR”“等 GitGud CI”“Windows 构建通过”或“部署重复全量测试”；历史回执保持原样。
