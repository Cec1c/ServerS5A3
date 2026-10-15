# 客户端 Release 同步与上线门槛

`Client Release sync` 将原作者的客户端 Release 导入本方仓库，并在本方 GitHub 与 GitGud fork 之间双向同步。它仅处理 `client-*` 标签及客户端 ZIP，保留精确标签提交、原始附件、预览状态、来源说明和完整 SHA256。原作者仓库保持只读来源；源码主分支、玩家更新源和运行服务由各自流程管理。

## 配置

在 GitHub 仓库设置以下变量与 secret：

| 名称 | 含义 |
| --- | --- |
| `GITGUD_RELEASE_PROJECT` | 本方 GitGud fork 的数字项目 ID；为空时禁用 |
| `GITGUD_RELEASE_UPSTREAM_PROJECT` | 原作者项目 ID，仅导入公开 Release |
| `GITGUD_RELEASE_TOKEN` | 只属于本方 fork 的 project access token，具有 API 与新标签/附件发布权限 |

GitHub 使用短期 `GITHUB_TOKEN`。工作流在手动运行、发布 Release 和每小时两次定时检查时执行；调度可能延迟。任务串行执行，从可信的 `master` 读取同步实现，不执行 Release 附件或来源仓库代码。

本机可使用 `--local-auth` 从已登录的 `gh` / `glab` 获取凭据，仅在内存中使用；CI 使用环境变量 `GH_TOKEN` / `GITGUD_RELEASE_TOKEN`。不要把 token 写入参数、日志或仓库。

```text
python scripts/ci/sync_client_releases.py --github OWNER/REPO --gitgud-project FORK_ID --upstream-project UPSTREAM_ID --cache runtime/client-release-cache --receipt runtime/client-release-sync.json
```

默认只检查和生成计划；正式执行增加 `--apply`。`--tag` 可限制具体版本，`--write-to github` 或 `--write-to gitgud` 可限制写入目标。省略原作者项目后，仍能在两个 fork 之间双向同步。

## 一致性与失败处理

- 同 tag、附件名、长度、摘要及标签提交完全相同则跳过。来源标记保留最初 Release 身份，重复同步不会互相制造新发行。
- 任意相同身份的附件或标签不同，在写入前停止整批计划；不覆盖附件、不移动标签、不强推。应调查差异并以明确的新版本发布。
- 写入前复核来源，写入后从目标重新下载并核对 ZIP 及其中 `DNF.exe` 的完整摘要。源 URL 限定到对应仓库的附件/包路径，下载不携带 API 凭据。
- GitHub 先上传到草稿，再公开；GitGud 先上传以内容摘要区分的 package，再创建 Release。中途失败会保留已传附件和回执，应检查后重跑；不删除既有发行来掩盖失败。
- 回执由 Actions 作为 artifact 保留。逐个仓库报告结果；不能以一个目标成功推断全部完成。

## EXE 依赖的强制上线顺序

服务端修复若依赖 DNF.exe 变更，按以下顺序执行，前一步的读回验证通过后才进行后一步：

1. 发布最终兼容 EXE 到 Release，并完成指定仓库镜像同步，记录 EXE 补丁谱系及服务端依赖提交。
2. 更新玩家下载器实际使用的更新源和通道，读回 Release 附件、签名清单与实际下载文件，核对完整 EXE SHA256、配套 PVF/dstr/NPK 和批准的登录器/服务端兼容 profile 一致。
3. 保存验证回执后，部署依赖该 EXE 的服务端构建，再核对运行二进制、健康与连接；原生游戏验收另行记录。

仅发布 Release、仅更新 preview 而玩家仍使用 stable、仅改配置摘要或本地测试通过均不满足第 2 步。任一环节未更新或摘要不一致就停止该次上线，保留当前兼容组合和回退产物。Release 镜像不会自动改写作者稳定通道或本服签名内容。

每次 EXE 补丁记录输入/输出 SHA256、大小、语义/位置/前后字节、配套资源、服务端依赖、验证范围、各发布目标和实际更新源读回结果。新上游版本逐项判断补丁已吸收、仍需迁移或冲突；不要套旧偏移或覆盖上游新增的接收位。

运行同步回归：

```text
python -m unittest discover -s scripts/ci -p "test_*.py" -v
```
