# 客户端 EXE Release 同步与上线门槛

`Client Release sync` 在上游 GitGud Release 与本方 GitHub Release 之间直接双向同步客户端 EXE 发布资产。GitGud fork 仅参与服务端源码贡献：先推 fork，再向上游提交 PR/MR；它不参与 Release 同步。

同步仅处理 `client-*` 标签及包含 `DNF.exe` 的原始 ZIP（可附说明与摘要），保留精确标签提交、附件、预览状态、来源说明和完整 SHA256。工作流不构建服务端，不复制服务端产物，不修改任何源码分支，也不切换玩家更新源或部署服务。

归档允许 `.txt`、`.md`、`.json` 说明，以及根级 `scripts/` 目录内（含子目录）的 `.py` 源码证据。源码仅按归档成员读取并校验 CRC，不解压、导入或执行；其他目录的 Python 文件和其他可执行类型仍拒绝。所有成员仍经过路径、大小、数量、重复与符号链接检查，且必须包含唯一的有效 `DNF.exe`，保留 ZIP 和 EXE 的完整摘要核验。

## 配置

在本方 GitHub 仓库设置以下变量与 secret：

| 名称 | 含义 |
| --- | --- |
| `GITGUD_CLIENT_RELEASE_PROJECT` | 获准发布 EXE 的上游 GitGud 数字项目 ID；为空时禁用。不要填写源码 fork ID。 |
| `GITGUD_CLIENT_RELEASE_TOKEN` | 仅属于上述项目的 project access token，具有 API 与新标签/附件发布权限 |

GitHub 使用短期 `GITHUB_TOKEN`。工作流在手动运行、发布 Release 和每小时两次定时检查时执行；调度可能延迟。任务串行执行，从可信的 `master` 读取同步实现，不执行 Release 附件或来源仓库代码。GitGud 的授权限于此次客户端 Release 流程；源码贡献仍走 PR/MR。

本机可使用 `--local-auth` 从已登录的 `gh` / `glab` 获取凭据，仅在内存中使用；CI 使用环境变量 `GH_TOKEN` / `GITGUD_RELEASE_TOKEN`。不要把 token 写入参数、日志或仓库。

新建 GitHub 标签须通过可信 Actions 的短期 `GITHUB_TOKEN`，避免旧标签内的历史工作流被个人凭据再次触发；本机仍可检查计划及处理已有标签。GitGud 标签推送使用 `ci.skip`，不触发服务端流水线。

```text
python scripts/ci/sync_client_releases.py --github OWNER/REPO --gitgud-project UPSTREAM_RELEASE_PROJECT_ID --cache runtime/client-release-cache --receipt runtime/client-release-sync.json
```

手动运行 `Client Release sync` 时可填写 `tag`，只验证和同步该版本；留空及定时触发仍检查全部版本。指定标签继续执行原有归档、来源、摘要及双端读回校验。

默认只检查和生成计划；正式执行增加 `--apply`。`--tag` 可限制版本，`--write-to github` 或 `--write-to gitgud` 可限制本次方向。默认核对两个仓库，向缺少对应发行的一方复制；没有第三个 Release 仓库。

## 一致性与失败处理

- 同 tag、附件名、长度、摘要、预览状态及标签提交完全相同则跳过。来源标记保留最初 Release 身份，重复同步不会制造新发行。
- 同名附件或标签冲突，在写入前停止整批计划；不覆盖附件、不移动标签、不强推。应调查差异并以明确的新版本发布。
- 全局预检包含裸 tag、孤立 package 和中断草稿；草稿的来源、预览位及已有资产摘要必须一致。
- 写入前复核来源，写入后从目标重新下载并核对 ZIP 及其中 `DNF.exe` 的完整摘要。源 URL 限定到对应仓库附件/包路径，下载不携带 API 凭据。
- GitHub 先上传到草稿，再公开；GitGud 先上传以内容摘要区分的 package，再创建 Release。中途失败保留已传附件和回执，检查后重跑；不删除既有发行。
- 回执由 Actions 作为 artifact 保留。逐个仓库报告结果；真实同步方向与模拟测试分别记录。

## EXE 依赖的强制上线顺序

服务端修复若依赖 DNF.exe 变更，按以下顺序执行，前一步读回通过后才进行后一步：

1. 最终兼容 EXE 完成上游 GitGud Release 与本方 GitHub Release 发布/同步，记录补丁谱系及服务端依赖提交；GitGud fork 无需复制 Release。
2. 更新玩家下载器实际使用的源和通道，读回 Release 附件、签名清单及实际文件，核对完整 EXE SHA256、配套 PVF/dstr/NPK 和批准的登录器/服务端 profile 一致。清单应声明最终派生 EXE，二次检查不应重新下载原版覆盖它。
3. 保存回执后，才允许按独立部署流程上线依赖该 EXE 的服务端构建，再核对运行二进制、健康与连接；原生游戏验收另行记录。

仅发布 Release、仅更新 preview 而玩家仍用 stable、仅改配置摘要或本地测试通过均不满足第2步。缺少更新或摘要不一致就停止该次上线，保留当前兼容组合和回退产物。此同步任务本身不构建或部署服务端。

每次 EXE 补丁记录输入/输出 SHA256、大小、语义/位置/前后字节、配套资源、服务端依赖、验证范围及各发布目标读回结果。新上游版本逐项判断补丁已吸收、仍需迁移或冲突，不套旧偏移或覆盖上游新增接收位。

运行同步回归：

```text
python -m unittest discover -s scripts/ci -p "test_*.py" -v
```
