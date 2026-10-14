# DNF90 Project Working Agreement

## Start Here

- Read `changlog.md` before changing code, deployment scripts, generated configuration, or runtime behavior.
- Keep `go-server` source-only. Runtime configuration, binaries, logs, PVF, database state, and PID files belong under `runtime`.
- Edit `runtime/config/instance.json` or `deploy/templates/instance.example.json`; do not hand-edit generated files under `runtime/configs`.
- Windows deployment entrypoints must stay BAT-only. Do not add PowerShell scripts or PowerShell calls.

## Source Boundaries

- `go-server/internal/app/dnf90` is the single composition root.
- `go-server/internal/services/dnfbridge` owns transport, session state, packet ordering, and current-client compatibility.
- `go-server/internal/modules/dnf` owns DNF domain rules and repository transactions.
- `go-server/internal/services/logic/dnf` owns repository assembly.
- Do not split the large bridge package merely for cosmetic directory changes. Extract only behind tests and current-client evidence.

## Data and Protocol Rules

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
