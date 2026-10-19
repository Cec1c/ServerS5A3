# DNF90 Project Working Agreement

## Start Here

- Read `changlog.md` before changing code, deployment scripts, generated configuration, or runtime behavior.
- Keep `go-server` source-only. Runtime configuration, binaries, logs, PVF, database state, and PID files belong under `runtime`.
- Edit `runtime/config/instance.json` or `deploy/templates/instance.example.json`; do not hand-edit generated files under `runtime/configs`.
- Windows deployment entrypoints must stay BAT-only. Do not add PowerShell scripts or PowerShell calls.

## 写作与结论表达

直接陈述结论、依据和行动，不使用重复免责、预设反驳和自我辩护。影响判断的前提、不确定性与未完成项保留一次，用具体事实说明。

## Seeker 研究与执行工作流

- 用户指定当前会话为 Seeker 或调用 Seeker 工作流时，按 [Seeker 工作流](Docs/SEEKER_WORKFLOW.md) 推进：Astra 取证、解释成因、在 `Docs/seeker/` 留可公开方案；DeepSeek Flash 执行明确的小任务；Astra 验收关键结果。
- 方案写清目标、必要文件、解决思路、测试与验收条件，简洁直接。明确告诉执行者：允许失败，返回已尝试方法、失败证据和下一步所需信息。
- 默认一次一个 DeepSeek Flash 执行子代理，`max`、最小上下文、不嵌套；复杂问题由 Seeker 处理，或交用户指定的同模型 Helper。当前任务的委派授权随交接记录保存，其他会话仍按用户当次指令启用。
- 任务板块优先主线相关问题；原文涂色表示其他人已完成，默认跳过且不计入本轮成果。PVF 写入交用户指定的 PVF-Writer；同步与发布交 Syncer，并传递本次授权范围及依赖。
- 可按证据需要使用代码/Git 搜索、日志与协议探针、真实 PVF 只读查询、REA/IDA/Capstone 静态分析、浏览器原格式读取和隔离测试。工具与模型以当前可用能力为准；Pebrel 面板先核对身份再派工。
- 公共方案只保留可复用技术结论；原始 BUG、截图、环境及面板身份保存在 ignored `local-notes/seeker/`。入口、任务状态和续接方法见该目录及 `local-notes/AGENT_HANDOFF.md`。

## Source Boundaries

- `go-server/internal/app/dnf90` is the single composition root.
- `go-server/internal/services/dnfbridge` owns transport, session state, packet ordering, and current-client compatibility.
- `go-server/internal/modules/dnf` owns DNF domain rules and repository transactions.
- `go-server/internal/services/logic/dnf` owns repository assembly.
- Do not split the large bridge package merely for cosmetic directory changes. Extract only behind tests and current-client evidence.

## Data and Protocol Rules

- 服务端修复若依赖 EXE 变更，必须先发布最终 EXE Release 并同步指定镜像，再核验玩家实际更新源/通道的签名清单、下载文件摘要及配套 PVF/dstr/NPK 与批准 profile 一致，最后部署依赖服务端。任一环节未更新或摘要不同即停止上线；preview 不能代替玩家仍使用的 stable。完整流程见 `Docs/CLIENT_RELEASE_SYNC.md`。

- Current EXE behavior is authoritative for uncertain packet layouts.
- Never invent inventory, character, database, PVF, channel, or map data to hide a missing dependency.
- The local profile is single-account and local-only. The 90CN bootstrap/channel download endpoint, admin endpoint, and MySQL stay on loopback. Dynamic channel-directory advertisements and every game port must use an automatically detected or explicitly configured private LAN IPv4 and bind only that local interface; never use a wildcard or public address.
- The `90cn-decode-bypass-v1` protocol profile, asset hashes, and client hashes are a compatibility unit. Change them together and record evidence.

## 客户端函数索引与静态取证

- 公共入口见 [客户端函数与协议索引](Docs/CLIENT_FUNCTION_INDEX.md)，机器可读记录在 `Docs/client-functions/`。该索引已获用户授权公开并同步；只收录模块摘要、RVA、脱敏语义及公共证据。原始反编译、私人报文和环境资料保存在被忽略的 `.cache/` 或 `local-notes/`。
- 复用前匹配完整模块 SHA256，区分函数入口、函数内片段和导出符号；新版本新增记录，不套用旧地址。协议方向、类别和证据限制以条目为准，不用候选地址或符号名证明业务功能。
- 优先从服务端代码、日志及匹配 PVF 定位；小窗口使用 Capstone，需要伪代码或跨函数关系时再用 REA/IDA。REA 的 IDA 提供者仍依赖相应 MCP 后端，工具接入不等于目标分析成功。
- 默认静态只读，不启动游戏/登录器或修改玩家原文件。分析结束显式关闭会话并核实所属 worker 释放；原始反编译留在 ignored 目录。索引文档变更核对摘要、地址、引用及隐私即可，不据此宣称重新部署或游戏验收。

## 独立 GM 仓库边界

- GM 的页面、鉴权、物品查询、福利扫描、批量任务和部署入口迁往 https://github.com/Cec1c/DNF90GM 。不得将其实现重新放回本仓。
- 服务端保留私有管理 API v1、邮件/资产事务与 `signupbenefits` 初始化政策。原表名、回执和注册额度快照保持兼容。
- 现有启用 GM 的部署 profile 需先按 `Docs/GM_STANDALONE.md` 迁移；仓库清理不停止线上 GM，不删除福利数据。
- 修改后执行 `python scripts/check_server_boundary.py`，检查已跟踪源码和游戏二进制的依赖边界。

## Validation commands

Run from `go-server`:

```text
gofmt -w <changed-go-files>
go list -buildvcs=false ./...
go test -buildvcs=false -count=1 ./internal/modules/dnf/...
go test -buildvcs=false -count=1 ./internal/services/dnfbridge
go test -buildvcs=false -count=1 ./cmd/server/control
go vet ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
go build -buildvcs=false -o ..\runtime\bin\DNF90Server.exe ./cmd/server/dnf90
```

Run deployment preflight without a database:

```text
..\deploy\windows\control.bat check --skip-database
```

## Documentation

- Record meaningful code, deployment, database, test, and compatibility changes in `changlog.md`.
- Never record plaintext generated passwords in `changlog.md`, logs, screenshots, commits, or support bundles.
- The requested runtime profile deliberately uses a plaintext `root` DSN. Keep it loopback-only and never commit generated runtime configuration.

- 客户端 EXE 发布资产在上游 GitGud Release 与本方 GitHub Release 之间直接双向同步；GitGud fork 仅用于源码贡献，不参与 Release 镜像。EXE 同步不构建或部署服务端，源码仍通过 fork 的 PR/MR 进入上游。

## Repository Boundary

- This online repository contains the game server only. The GM tool is an independent local system.
- Do not commit GM UI, HTTP tool implementation, embedded startup wiring, tool configuration, or tool releases here.
- Preserve local GM files and changes. Repository cleanup must not delete the user's local tool.
- Shared gameplay/domain rules and server administration infrastructure may remain when used by the server; do not delete gameplay logic merely because GM also calls it.

## 源码唯一权威与贡献

- GitGud 上游 master 是唯一权威源；所有独立功能、修复、测试、文档和 CI 分支均通过 MR 进入上游，存在同项 MR 时更新原 MR。
- 同步 Agent 自行妥善解决冲突，保留双方有效逻辑，验证后非强推更新；有用户授权时按评审要求合并，并读回上游最终 SHA。
- GitHub 仅由 CI 单向快进镜像上游确切提交，不在 GitHub 开发或合并同步 PR，不反向推送 GitGud。GitGud fork 仅用于贡献分支；不能把仅推 fork 当作贡献完成。
- 详见 Docs/REPOSITORY_SYNC.md。源码、Release、玩家通道、服务端累计部署和实机验收分别记录；私有累计源码不得直接上传公开上游。
