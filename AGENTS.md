# DNF90 Project Working Agreement

## Start Here

- **每次新部署必须当场拉取上游**：在本次部署 run 内，对专用干净权威主干工作树执行 `git pull --ff-only`，来源必须是 GitGud 上游 `master`，并记录执行时间、来源、拉取前后 SHA。核对候选已集成最新主干及有效改动，同时保留线上累计修复和本服扩展。拉取失败、来源不符、分叉或漏接更新时，在打包、上传、切换前拒绝执行。构建完成后再次拉取；有新提交则重新集成和验证。历史拉取回执、本地旧引用或口头确认不能替代本次检查。
- **单独发布客户端也必须先 pull**：每次客户端更新、构建发布准备及仅客户端发布均执行上述权威主干实际拉取、证据记录、独立集成与固定提交构建检查；发布前再核对上游和实际玩家通道，有变化则重新集成验证。禁止直接将当前工作区或旧构建目录打包发布。
- 已授权远端更新使用 `GAME_UPDATE_REMOTE.bat`，先 inspect actual active，再由 deploy 强制核对最新主干、完整范围、测试构建、备份、运行摘要、签名与频道；私有 profile 和累计基线不上传。工具及恢复来源约束见 [Docs/GAME_UPDATE.md](Docs/GAME_UPDATE.md)。

- Read `changlog.md` before changing code, deployment scripts, generated configuration, or runtime behavior.
- 部署前先读 [外部频道表选择与验收](Docs/CHANNEL_TABLE_DEPLOYMENT.md)，按仓库118频道CSV核对已批准的完整外部表，首次prepare就显式传入 `--channel-info`。不要默认提取PVF内置表作为部署目录；升级PVF时保留现有外部表。CSV仅供核对，运行输入必须是完整ETC，并读回实际进程路径、摘要、端口及客户端目录。
- Keep `go-server` source-only. Runtime configuration, binaries, logs, PVF, database state, and PID files belong under `runtime`.
- Edit `runtime/config/instance.json` or `deploy/templates/instance.example.json`; do not hand-edit generated files under `runtime/configs`.
- Windows deployment entrypoints must stay BAT-only. Do not add PowerShell scripts or PowerShell calls.

## 写作与结论表达

直接陈述结论、依据和行动，不使用重复免责、预设反驳和自我辩护。影响判断的前提、不确定性与未完成项保留一次，用具体事实说明。

## Seeker 研究与执行工作流

- **先自主调查，再请求补充**：可识别的功能和现象即可开始；自行从代码、匹配PVF/客户端及现成证据定位并尝试复现，必要时建立最小夹具，按问题检查执行者与队友/观察者链路。缺日志或一次未复现不构成 `needs_info` 理由；仅在实际尝试后仍有不可自行还原的关键条件或已核实的基础设施缺口，且替代方法仍无法继续时，记录尝试、结果和具体卡点，再问最少必要项。续接时重审仅因缺日志挂起的旧条目，细则见 [BUG持续流水线](Docs/seeker/ISSUES_API_WORKFLOW.md)。
- 论坛反馈按 [BUG持续流水线](Docs/seeker/ISSUES_API_WORKFLOW.md) 先查重、判断合理性/功能BUG和复现信息，再按影响校正优先级；选中批次分派当前账号并回复日期流水线，修复验证后按授权上传合并MR，在原反馈回写分支、MR、合并SHA及验收状态。
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

## anti-RE 使用边界与客户端发布要求

- anti-RE 是独立的 EXE 加锁、防篡改工具，按黑盒 CLI 使用。默认只读取 README、CLI 帮助和输入/输出、验证契约；非任务确需不深入源码、反编译或保护机制，不将其内部实现引入业务调查上下文，也不绕过工具报错或校验。
- 后续客户端 Release 中的所有 EXE 都必须经过 anti-RE 加锁并通过工具规定的验证，包括正式、preview、热修复、独立 EXE 包及整包/分卷内的 EXE。未加锁的构建、补丁中间文件和调试 EXE 不得作为 Release 附件或进入玩家更新源。
- 顺序固定为：完成功能修改 → anti-RE 加锁 → 按 CLI 契约验证最终文件 → 发布 Release 并同步指定镜像 → 更新玩家实际通道 → 部署依赖服务端。加锁后再改 EXE 必须重新加锁和验证。回执绑定工具版本、验证结果及最终 EXE 完整 SHA256/长度，其他字段按真实 CLI 契约记录；文件名、MZ 文件头、签名清单或摘要一致不能替代加锁验证。
- 玩家更新源必须使用所选正式/预览通道最新已发布 Release 中的已加锁 EXE。每次更新当场核对权威发行列表、通道、标签与附件，排除草稿，不仅依赖缓存或 `latest` 指针；最新发行缺少加锁证明或验证失败就停止切换，不静默选旧发行或未加锁原版。本服派生修改也须先发布新的已加锁 Release，再切更新源。
- 维护或使用一键更新、发布、镜像及更新源入口时，必须检查现有 BAT/Python、CI、Release 镜像、Hub、签名清单和实际下载链是否落实上述要求。重点检查 `GAME_UPDATE_REMOTE.bat`、`deploy/windows/game_update.py`、`scripts/ci/sync_client_releases.py`、`.github/workflows/client-release-sync.yml` 及实际更新源所属仓库。记录已接入的校验和仍缺的检查；缺少有效加锁验证或最新发行来源绑定时先补齐，不能仅凭旧脚本成功宣称新发布合规。
- 镜像必须原样使用已验证的加锁产物，读回 Release 附件、玩家签名清单和实际下载文件并核对同一摘要。历史未加锁或加锁状态未确认的发行不得重新镜像、回填到另一端或设为新更新源。旧 Release 清理由用户指定的独立进程负责；本流程不自行删除。现有运行组合与回退产物保留，不据本条自动切换或重启服务。

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
