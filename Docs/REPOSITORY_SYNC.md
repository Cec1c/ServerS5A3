# GitHub 权威源码与 GitGud 异步镜像

公开源码、审查和 CI 统一到 https://github.com/Cec1c/ServerS5A3 master。贡献分支提交 GitHub PR；GitGud master 仅由 Repository sync 非强推快进，任何镜像失败与排队均不阻塞 PR、下游同步或部署。

## 贡献和镜像

1. 从最新公开 GitHub master 创建干净 fix/ 分支，保留本机脏文件，提交独立变更记录及必要验证。
2. 更新同项 GitHub PR，合并后记录实际 merge SHA。旧 GitGud MR 的尚未包含差异迁移为 GitHub PR，关联历史，保留作者贡献。
3. Repository sync 在 master push、每小时及手动触发时将 GitHub 确切 master 推到 GitGud。相同 SHA 不写入；镜像领先/分叉、任一引用变化则停止。绝不强推、删除分支、移动标签或反向覆盖 GitHub。
4. 下游群服从公开 GitHub 合并并保留私有累计；私有内容不进入公开库。源码、镜像、资源、部署与玩家验收分别记录。

## 配置

GitHub 变量 `GITGUD_MIRROR_REPOSITORY` 指向镜像 HTTPS 仓库。Secret `GITGUD_SOURCE_MIRROR_TOKEN` 仅在受信主干镜像 job 中使用，并限定 Git 凭据的 GitGud URL 范围。PR job 不接触镜像或发布凭据。停用旧 GitGud→GitHub源码同步；SOURCE_MIRROR 身份仍被独立客户端 Release 标签同步使用，不删除。

GitGud 自动 push/MR CI 已禁用；可手动运行镜像提示，不构建 Windows，不作为任何正式发布 gate。Linux Verify 只在 GitHub PR/手动运行；源码、工具、容器和文档按 diff 分流，普通 push 不重复验证，同 PR 过时纯验证可取消。

回归入口：`python -m unittest discover -s scripts/ci -p test_sync_repositories.py -v`。使用临时本地 Git 仓库检查方向、分叉、并发变更和拒绝强推。

完整跨开发机说明见 [GitHub 权威迁移](GITHUB_AUTHORITY_MIGRATION.md)。客户端 EXE Release 仍沿用独立资源发布流程，此次源码迁移不重发资源或中断服务。
