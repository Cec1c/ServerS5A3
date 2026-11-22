<div align="center">

<img src="Docs/images/readme-mascot.jpeg" alt="ServerS5A3 DNF90 Go 游戏服务端" width="460">

# ServerS5A3

[![语言：Go 1.26.5](https://img.shields.io/static/v1?label=%E8%AF%AD%E8%A8%80&message=Go%201.26.5&color=00ADD8&style=flat-square&logo=go&logoColor=white)](go-server/go.mod) [![存储：MariaDB 11.8](https://img.shields.io/static/v1?label=%E5%AD%98%E5%82%A8&message=MariaDB%2011.8&color=003545&style=flat-square&logo=mariadb&logoColor=white)](Docs/BUILD_AND_DEPLOY.md#先把东西备齐) [![运行：Docker Linux](https://img.shields.io/static/v1?label=%E8%BF%90%E8%A1%8C&message=Docker%20Linux&color=2496ED&style=flat-square&logo=docker&logoColor=white)](Docs/DOCKER_RUNTIME_REFERENCE.md) [![CI](https://img.shields.io/github/actions/workflow/status/Cec1c/ServerS5A3/verify.yml?branch=master&style=flat-square&label=CI)](https://github.com/Cec1c/ServerS5A3/actions/workflows/verify.yml) [![许可：GPL-3.0](https://img.shields.io/static/v1?label=%E8%AE%B8%E5%8F%AF&message=GPL-3.0&color=2563EB&style=flat-square)](LICENSE) [![Stars](https://img.shields.io/github/stars/Cec1c/ServerS5A3?style=flat-square&label=stars)](https://github.com/Cec1c/ServerS5A3/stargazers) [![Release](https://img.shields.io/github/v/release/Cec1c/ServerS5A3?include_prereleases&style=flat-square&label=release)](https://github.com/Cec1c/ServerS5A3/releases)

面向 `official-24.9` 客户端的 DNF90 Go 游戏服务端：在 Linux 容器环境中运行游戏服务与 MariaDB 存档，支持 Windows 玩家通过登录器连接。

[构建与部署指南](Docs/BUILD_AND_DEPLOY.md) ｜ [Docker 运行配置包必读](Docs/DOCKER_RUNTIME_REFERENCE.md) ｜ [常见假 BUG 速查](#常见假-bug-与运行开关速查) ｜ [120 频道表必读](Docs/CHANNEL_TABLE_DEPLOYMENT.md) ｜ [PVF 与配套客户端](https://github.com/Cec1c/ServerS5A3/releases)

[PurpleMagic 登录器与 GM](https://github.com/Cec1c/PurpleMagic) ｜ [报告问题](https://github.com/Cec1c/ServerS5A3/issues/new/choose) ｜ [参与贡献](CONTRIBUTING.md)

</div>

> [!IMPORTANT]
> **切勿裸跑 `DNF90Server` 二进制或套用旧一键端配置。** 本仓大量已修复的协议与玩法（包括**青龙/黄龙/魂大会刷怪**、V212–V215 双组 Buff 登记与换装重算、史诗分解结果弹窗、城镇同屏加载、公会系统与深渊/组队兼容项）在裸程序源码中默认处于保守关闭态，**必须通过 `deploy/docker` 配置生成器合并推荐预设包 [`docker-instance.recommended.json`](deploy/templates/docker-instance.recommended.json)；首次 `prepare` 默认使用与私有部署一致的 [120 频道外部 ETC](Docs/CHANNEL_TABLE_DEPLOYMENT.md)，并校验完整摘要**。公开源码与 PR 唯一权威入口为 [GitHub Cec1c/ServerS5A3](https://github.com/Cec1c/ServerS5A3)，GitGud 仅作异步镜像。

## 常见假 BUG 与运行开关速查

社区中高频复现的许多问题其实早已在 `master` 修复，卡住的根本原因是**忽略了仓库的 Docker 运行配置包、漏合推荐 `instance.json` 开关，或客户端/PVF 未按 Release 配对**。遇到以下现象请先核对运行配置：

| 现象（常被误认为代码未修） | 真实原因 | 解决方法（写入 `runtime/docker/instance.json` 后重跑 `up`） |
| --- | --- | --- |
| **青龙大会 / 黄龙大会 / 魂大会进图不出怪、直接判通关或翻牌卡住** | 源码默认 `dungeon_tournament` 为 `off`，未走 `[tournament enemies]` 4 轮赛程铺怪路径 | 设 `"dungeon_tournament": "vendor"`（对应 `DNFBRIDGE_DUNGEON_TOURNAMENT=vendor`；**填 `on`/`true`/`1` 均无效，只认 `vendor`**） |
| **选角或进城后客户端闪退（赛马活动冲突）** | 首次 `prepare` 模板默认开启了尚未修复闪退的 2365 赛马活动 | 设 `"joust_event_enabled": false`；**旧数据库**还必须通过管理接口将活动 **2365** 保存为关闭并重启（库内保存值优先于环境变量） |
| **登录后无频道、选频道转圈或无法进游戏** | 旧实例仍在使用不同频道表或PVF内置目录 | 新实例默认使用统一外部表；旧实例按迁移说明备份并显式选择 `--channel-info go-server/internal/services/dnfbridge/testdata/channel_info.etc`（[120 频道说明](Docs/CHANNEL_TABLE_DEPLOYMENT.md)） |
| **V212–V215 双组 Buff 登记不保存、切图清空或换装不重算** | 未开启双组 Buff 同步或换装重算开关，或客户端与开关未配对 | 配套 V212–V215 客户端设 `"buff_group_sync": "on"` 与 `"buff_swap_reapply": "on"`（详见 [Buff 部署说明](Docs/BUFF_GROUP_DEPLOYMENT.md)） |
| **史诗分解不弹无色/灵魂结果框（V210+）、城镇非队友加载慢或异常（V211+）** | 漏开与新版客户端配套的服务端显示模式 | 设 `"disjoint_epic_display": "paired-v1"` 与 `"town_display_only": "on"` |
| **公会排行/红包/守护神/精髓无法使用，或深渊锁槽、组队进图掉线/顺序错乱** | 公会子系统默认全关，且未注入 `runtime_compatibility` 17 项协议修复 | 直接将 [`deploy/templates/docker-instance.recommended.json`](deploy/templates/docker-instance.recommended.json) 合并进 `instance.json` |
| **NPC 赠礼（好感度 op812）不刷新、黑钻 188 疲劳 / 加百利商店缺失** | 服务端提交过旧，或客户端/PVF 未同步到 2026-11-15 公开批次 | 拉取最新 `master` 重新构建，并在 [GitHub Releases](https://github.com/Cec1c/ServerS5A3/releases) 下载 **V215 客户端组件** 与 **`66482976` 正式服完整 PVF**（见 [公开功能清单](Docs/SELECTED_PUBLICATION_20261115.md)） |

完整的环境变量映射与线上运行进程只读核对脚本见 **[Docker 运行配置与脱敏参考](Docs/DOCKER_RUNTIME_REFERENCE.md)** 及参考文件 [`deploy/templates/docker-server.redacted.env`](deploy/templates/docker-server.redacted.env)。

## 快速开始：使用 Docker 运行配置包

### 方式一：把仓库和配置包交给编程 Agent 自动部署（推荐）

准备一台装有 Docker Engine、Compose v2 与 Python 3.10+ 的 Linux 服务器，以及配套的 Windows 客户端目录，把下面这段话发给你的 Agent（详细步骤见[《大肥鱼也能看得明白的构建与部署说明》](Docs/BUILD_AND_DEPLOY.md)）：

```text
请阅读这个项目的 AGENTS.md、Docs/BUILD_AND_DEPLOY.md、Docs/DOCKER_RUNTIME_REFERENCE.md 和 Docs/CHANNEL_TABLE_DEPLOYMENT.md，
帮我在指定的 Linux 服务器部署一套 DNF90。
服务器连接信息和 Windows 客户端目录由我提供。

要求：
1. 先核对目标机器、源码提交、数据库容量以及客户端 DNF.exe / Script.pvf 的 SHA256 配对关系。
2. 使用 deploy/docker/manage.sh 构建镜像并执行首次 prepare，默认使用 go-server/internal/services/dnfbridge/testdata/channel_info.etc（统一120频道外部表）；显式指定的表须匹配同一摘要，不能提取PVF目录代替。
3. 将 deploy/templates/docker-instance.recommended.json 合并进生成的 runtime/docker/instance.json（保留自动生成的本机地址、密码与令牌），确保启用 dungeon_tournament="vendor"（青龙/黄龙/魂大会刷怪）、buff_group_sync="on"、buff_swap_reapply="on"、disjoint_epic_display="paired-v1"、town_display_only="on"、公会功能及 runtime_compatibility 全部推荐项，并将 joust_event_enabled 设为 false 关闭 2365 赛马活动。
4. 启动容器后，按 Docs/DOCKER_RUNTIME_REFERENCE.md 读取实际 DNF90Server 进程的 /proc/<pid>/environ 与活动生效状态核对无误。
5. 给我一个能注册、登录、选频道并启动游戏的登录器（或接入 PurpleMagic），留下启停、日志、备份、更新与回退命令。
```

### 方式二：在 Linux 服务器手动执行

将 `/srv/dnf-assets/Script.pvf` 替换为上传后的真实 PVF 路径，`192.168.1.50` 替换为这台服务器网卡上实际存在、玩家可达的私网 IPv4：

```sh
# 1. 构建镜像并初始化运行目录（显式选定 120 频道外部 ETC）
sh deploy/docker/manage.sh build
sh deploy/docker/manage.sh prepare \
  --pvf "/srv/dnf-assets/Script.pvf" \
  --channel-info "go-server/internal/services/dnfbridge/testdata/channel_info.etc" \
  --host-ip 192.168.1.50

# 2. 一键合入官方推荐运行配置包（保留 prepare 生成的独立数据库密码、IP 与令牌）
python3 - <<'PY'
import json
from pathlib import Path

inst_path = Path("runtime/docker/instance.json")
rec_path = Path("deploy/templates/docker-instance.recommended.json")
inst = json.loads(inst_path.read_text(encoding="utf-8"))
rec = json.loads(rec_path.read_text(encoding="utf-8"))
compat = {**inst.get("runtime_compatibility", {}), **rec.get("runtime_compatibility", {})}
merged = {**inst, **rec, "runtime_compatibility": compat}
inst_path.write_text(json.dumps(merged, indent=2) + "\n", encoding="utf-8")
PY

# 3. 重新生成 server.env / compose.json 并启动容器
sh deploy/docker/manage.sh up
sh deploy/docker/manage.sh status
curl http://127.0.0.1:17002/api/status
```

> [!TIP]
> 如果在 **4 核 16 GiB** 同机混跑游戏服务与 MariaDB，可在 `runtime/docker/instance.json` 额外加入 `"performance_profile": "shared-4c-16g"`（自动配置游戏容器 `10g` / `GOMEMLIMIT=8GiB`、数据库容器 `3g` / InnoDB 缓冲池 `1g`）；百人负载还可通过管理维护命令初始化公会持久指纹触发器与升级奖励恢复索引，详见 [优化表结构维护](Docs/OPTIMIZATION_SCHEMA_MAINTENANCE.md) 与 [热点路径说明](Docs/PERFORMANCE_HOT_PATHS_20261114.md)。

## 当前活动开放与赛马关闭说明

> [!WARNING]
> **截至目前，2365「竞技大赛（马战竞猜／赛马）」活动仍可能导致客户端闪退，必须保持关闭。** 首次部署通过上面的推荐预设包已将 `joust_event_enabled` 设为 `false`；若是**升级已有旧数据库**，数据库中保存的活动状态优先级高于环境变量，还必须通过 GM 或游戏管理接口把活动 **2365** 保存为 `enabled=false` 并重启游戏容器，详见[活动开关与赛马关闭方法](Docs/BUILD_AND_DEPLOY.md#当前活动开关与赛马关闭方法)。

当前源码在未覆盖时默认开启史诗之路（350）、累计在线奖励（2347）、普通签到（331）、每周签到（2208）和每日签到（2370）；新学期累计奖励（216）和红尾乔纳森（2395）默认关闭。

## 下载配套 PVF 与客户端 EXE

**`Script.pvf`、客户端 `DNF.exe` 及配套组件均发布在 [GitHub Releases](https://github.com/Cec1c/ServerS5A3/releases)**（镜像见 [GitGud Releases](https://gitgud.io/16ww/servers5a3/-/releases)）。注意进入 Releases 列表页同时查看 **Latest** 与 **Pre-release（预发行）**：

| 资源类型 | 当前公开版本参考 | 配对与使用说明 |
| --- | --- | --- |
| **当前正式服完整 PVF** | `pvf-current-server-20261115-66482976`（Pre-release） | SHA256 `66482976…`，包含最新黑钻、加百利、团本与累计玩法资源；部署时保持文件名为 `Script.pvf`，玩家端、服务端与 GM 必须使用同一份 PVF |
| **最新正式客户端组件** | `client-v215-blackdiamond-npc-formal-20261115`（Pre-release） | `DNF.exe` SHA256 `28e04d1d…`（含配套双 DLL 与界面 NPK），支持黑钻 188 疲劳、加百利商店、NPC 好感度与双组 Buff，细节见 [2026-11-15 公开说明](Docs/SELECTED_PUBLICATION_20261115.md) |
| **已验收稳定客户端基线** | `client-v212-accepted-cumulative-20261110`（Latest） | `DNF.exe` SHA256 `69ea3a8b…`，含双组 Buff、擂台与安徒恩累计修复；不含 11 月 15 日新增黑钻/加百利客户端消息门 |

分卷附件请按对应 Release 说明下载齐全后解压，不要用单 EXE 裸替换跳过配套 DLL/NPK 校验（配对准入规则见 `deploy/remote/client_pairing.py`）。

## 项目结构、登录器与 GM 边界

| 模块 | 职责 |
| --- | --- |
| 游戏服务 (`cmd/server/dnf90`, `dnfbridge`) | 处理账号登录令牌、频道引导与连接、进城同屏、组队 UDP 中继和副本战斗协议 |
| 领域与存储 (`internal/modules/dnf`) | 实现角色、背包、装备、任务、邮件、拍卖、公会、团本等规则与 MariaDB 增量事务 |
| 构建与容器配置 (`deploy/docker`, `deploy/templates`) | 提供 Linux Docker 镜像、实例配置生成器 `manage.py`、推荐开关预设包与脱敏环境参考 |
| 协议与调查资料 (`Docs/`) | 收录客户端函数索引、玩法/协议取证记录、性能优化与发布维护规范 |

```text
go-server/cmd/server/                   服务端、控制器与环境预检工具入口
go-server/internal/app/dnf90/           唯一应用组装与依赖接线
go-server/internal/services/dnfbridge/  网络会话、包序列与客户端协议适配
go-server/internal/modules/dnf/         游戏领域规则、仓储契约与 MySQL 实现
go-server/internal/services/logic/dnf/  仓储与领域服务组装
go-server/internal/platform/            事件循环、指标与基础框架组件
go-server/pkg/                          公共基础包
deploy/docker/                          Docker Linux 镜像与配置生成器 manage.py
deploy/templates/                       推荐 instance.json 预设与 server.env 参考
Docs/                                   部署指南、运行配置参考、协议索引与修复档案
runtime/                                Git 忽略的本地运行配置、PVF、日志与数据
dist/                                   构建产物输出目录
```

**服务端与 GM / 登录器的仓库边界**：本仓库专注维护游戏服务端，不再内嵌启动旧版 GM Web 面板（旧配置中的 `server.gmListen` 仅作兼容忽略）。玩家登录器、自动更新网关与独立 Web GM 推荐使用 **[PurpleMagic](https://github.com/Cec1c/PurpleMagic)**，也可按登录令牌协议自行实现：

- [PurpleMagic 中文介绍](https://github.com/Cec1c/PurpleMagic/blob/main/README.zh-CN.md)
- [网关、独立 GM 与同代 PVF 索引部署](https://github.com/Cec1c/PurpleMagic/blob/main/docs/DEPLOYMENT.zh-CN.md)
- [网关接入契约](https://github.com/Cec1c/PurpleMagic/blob/main/docs/GATEWAY_INTEGRATION.md) · [构建自定义登录器](https://github.com/Cec1c/PurpleMagic/blob/main/docs/LAUNCHER_INPUTS.md)

## 本地构建与按范围验证

安装 [Go 1.26.5](https://go.dev/dl/) 后，可在仓库根目录跨平台构建二进制产物：

```sh
# Linux / macOS
sh build.sh linux amd64

# Windows 命令提示符或 PowerShell
.\build.bat
.\build.bat linux amd64
```

产物输出到 `dist/<GOOS>-<GOARCH>/`（包含游戏服务 `DNF90Server`、本地控制器 `DNF90Control` 与预检工具 `DNF90Doctor`）。

> [!CAUTION]
> **构建脚本只生成可执行文件，不会自动生成运行配置或注入环境变量。** 直接双击或裸跑 `dist/*/DNF90Server` 会以源码最保守的回退默认值运行（导致青龙/黄龙大会零怪、双组 Buff 失效、频道表不一致等）。运行环境请统一交给 `deploy/docker/manage.sh`（或 `DOCKER.bat`）管理；旧 `deploy/templates/instance.example.json` 仅限早期 `90cn-decode-bypass-v1` 便携测试，不能替代当前 Docker `official-24.9` 配置。

修改代码后的验证默认复用 Go 测试缓存、只跑受影响包并执行完整 Linux 服务端构建（详见 [本地按范围验证说明](Docs/LOCAL_CICD.md)）：

```sh
# Linux 原生按范围验证
sh scripts/ci/test_core.sh --package ./internal/modules/dnf/guild/...
sh scripts/ci/test_core.sh --package ./internal/services/dnfbridge --run '^Test(Auction|TownPeerDelivery)'

# Windows 通过 Docker 只读挂载执行 Linux 按范围验证
LOCAL_VERIFY.bat --package ./internal/modules/dnf/inventory/... --output runtime/verify-inventory

# Docker 配置生成器与模板回归测试
python3 -m unittest discover -s deploy/docker -p "test_*.py" -v
```

需要无缓存全量回归时再显式传入 `--full --fresh`（或 `GO_TEST_UNCACHED=1`）。提交 PR 前请按 [变更记录规范](Docs/CHANGELOG_WORKFLOW.md) 在 `changelog.d/` 添加独立变更片段。

## 文档索引

| 任务场景 | 文档入口 |
| --- | --- |
| 从零搭建测试服 / 复制许愿词给 Agent | [《大肥鱼也能看得明白的构建与部署说明》](Docs/BUILD_AND_DEPLOY.md) |
| 核对 Docker 环境变量、青龙/黄龙大会与推荐开关包 | [Docker 运行配置与脱敏参考](Docs/DOCKER_RUNTIME_REFERENCE.md) · [`docker-instance.recommended.json`](deploy/templates/docker-instance.recommended.json) |
| Windows 本机 Docker 测试环境 | [Docker Linux 本地测试环境](Docs/DOCKER_LOCAL.md) |
| 选择与核验 120 频道外部 ETC | [频道表部署说明](Docs/CHANNEL_TABLE_DEPLOYMENT.md) |
| V212–V215 双组 Buff 协议配对 | [V212 双组 Buff 部署配置](Docs/BUFF_GROUP_DEPLOYMENT.md) |
| 查看近期公开的黑钻/加百利/好感度与高峰优化 | [2026-11-15 公开说明](Docs/SELECTED_PUBLICATION_20261115.md) · [2026-11-11 玩法增量](Docs/GAMEPLAY_UPDATES_20261111.md) · [热点优化](Docs/PERFORMANCE_HOT_PATHS_20261114.md) |
| 数据库持久指纹触发器与升级奖励索引维护 | [优化表结构维护](Docs/OPTIMIZATION_SCHEMA_MAINTENANCE.md) |
| 本地靶向测试、变更记录与 PR 协作流程 | [本地 CI/CD](Docs/LOCAL_CICD.md) · [变更记录规范](Docs/CHANGELOG_WORKFLOW.md) · [GitHub 权威迁移](Docs/GITHUB_AUTHORITY_MIGRATION.md) |
| 客户端协议逆向索引与历史调查档案 | [客户端函数索引](Docs/CLIENT_FUNCTION_INDEX.md) · [Seeker 调查方案](Docs/seeker/README.md) · [历史 changlog.md](changlog.md) |

## 上游致谢与许可证

本项目起源于 [16ww/servers5a3](https://gitgud.io/16ww/servers5a3)，当前公开权威仓库为 [GitHub Cec1c/ServerS5A3](https://github.com/Cec1c/ServerS5A3)，根目录构建脚本与产物目录组织参考了 [rewio/ServerS4A21](https://gitgud.io/rewio/ServerS4A21)。感谢上游作者与所有贡献者持续完善协议和玩法。

本项目采用 **GNU General Public License v3.0（GPL-3.0-only）** 开源，完整条款见 [LICENSE](LICENSE)；第三方组件保留各自原有许可证与版权声明。

---

## 组队/结算/回城修复移植（来自 DNF90_Server）

本仓库包含从 [DNF90_Server](https://github.com/tingfeng-h/DNF90_Server) 移植的一批客户端兼容修复，
目标是让 V211 客户端（`DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9`）在组队、结算翻牌、回城
这几条链路上行为正确。

详细说明（**移植步骤、冲突处理约定、每个问题的根因、运行时开关、验证方法**）见：
[`Docs/PARTY_PORT_FROM_DNF90.md`](Docs/PARTY_PORT_FROM_DNF90.md)

配套运行时配置（脱敏）见：`runtime-snapshot/20261122-upstream-test/`。

### 快速索引

| 问题 | 修复要点 | 开关 |
|---|---|---|
| 中途退图→回城踢人/血条残留 | 退图者 kind2 只在 owner=0；回城用 kind3 静默退役 | — |
| 回城后红电脑 | 删除空 kind2 复位（避免 `wasBound 1→0` 触发 `closeAll`） | — |
| 副本内退人后"已退出队伍" | 副本内不发空 kind2；中继只 sync | — |
| 城镇反复踢人/重组 | mode15 双守卫、先建 CUser 再发 op11、清 portOwners | — |
| 队友 actor 重复补投/战斗卡顿 | 关闭 ARQ1 重投链、op37 不重投、换房不重投 | `DNFBRIDGE_DUNGEON_PEER_RECOVERY` 等（默认 off） |
| 进图/换房慢 | 放行期共享角色快照 + 缓存 + 分阶段计时 | — |
| 打完 BOSS 不翻牌 | 结算/卡牌链路修复（上游原版在 V211 上失效） | — |
| 选图→回城空血条 | 回城/城镇投影补齐 | — |
| 城镇同队队员右键不弹菜单 | 补发 0x0007 选择通知 | `DNFBRIDGE_SAME_PARTY_TOWN_SELECTION_NOTICE`（默认 off，V211 建议 on） |

