<div align="center">

<img src="Docs/images/readme-mascot.jpeg" alt="老实写代码，很枯燥吧" width="480">

# ServerS5A3

**DNF90 Go 游戏服务端**

[![Go](https://img.shields.io/badge/Go-1.26.5-00ADD8?style=flat-square&logo=go&logoColor=white)](go-server/go.mod) [![MariaDB](https://img.shields.io/badge/Storage-MariaDB-003545?style=flat-square&logo=mariadb&logoColor=white)](Docs/BUILD_AND_DEPLOY.md#先把东西备齐) [![Docker](https://img.shields.io/badge/Deploy-Docker-2496ED?style=flat-square&logo=docker&logoColor=white)](Docs/BUILD_AND_DEPLOY.md#在-linux-上启动一套测试服) [![License: GPL v3](https://img.shields.io/badge/License-GPL%20v3-2563EB?style=flat-square)](LICENSE) [![Stars](https://img.shields.io/github/stars/Cec1c/ServerS5A3?style=flat-square)](https://github.com/Cec1c/ServerS5A3) [![Release](https://img.shields.io/github/v/release/Cec1c/ServerS5A3?style=flat-square)](https://github.com/Cec1c/ServerS5A3/releases)

在 Linux 上运行游戏服务与存档，让 Windows 玩家通过登录器进入阿拉德。

[构建与部署](Docs/BUILD_AND_DEPLOY.md) ｜ [PVF 与配套 EXE](https://github.com/Cec1c/ServerS5A3/releases) ｜ [登录器与 GM](https://github.com/Cec1c/PurpleMagic) ｜ [GitGud 上游](https://gitgud.io/16ww/servers5a3)

</div>

## 推荐安装方法

**把整个项目丢给 Agent，告知它服务器 IP 和密码，然后开始许愿。**

至于 Agent，请看这份[《大肥鱼也能看得明白的构建与部署说明》](Docs/BUILD_AND_DEPLOY.md)。里面有准备清单、可以直接复制的许愿模板、构建命令，以及开服后的验收步骤。

比如：

```text
请阅读这个项目的 AGENTS.md 和 Docs/BUILD_AND_DEPLOY.md，
帮我在指定的 Linux 服务器部署一套 DNF90。
服务器连接信息和 Windows 客户端目录由我提供。
先确认机器、现有服务和客户端配套，再完成构建、配置、备份与部署。
给我一个能注册、登录、选频道并启动游戏的基础登录器，
最后留下启动、停止、备份、更新和回退方法。
```

## 下载 PVF 和客户端 EXE

**`Script.pvf` 和对应版本的 `DNF.exe` 都放在 Release 附件中。** 打开 [GitHub Releases](https://github.com/Cec1c/ServerS5A3/releases) 或 [GitGud 上游 Releases](https://gitgud.io/16ww/servers5a3/-/releases)，按同一版本的说明下载配套文件。分卷附件按该版本说明下载齐全后解压。

> [!IMPORTANT]
> 客户端 EXE、PVF、频道表及相关 dstr/NPK 资源按所选 Release 配套使用。玩家客户端、游戏服务和 GM 使用的 PVF 需要一致。

## 项目里有什么

| 部分 | 用途 |
| --- | --- |
| 游戏服务 | 处理账号会话、频道连接、角色进城、组队和副本协议 |
| 领域模块 | 实现角色、背包、任务、装备、邮件、交易、公会等规则与数据事务 |
| 存档 | 使用 MySQL / MariaDB 保存账号、角色与游戏进度 |
| 构建与部署工具 | 提供跨平台构建脚本、Docker 配置生成器、实例控制器和环境检查工具 |
| 协议与回归资料 | 保存匹配客户端的函数索引、修复方案和自动化测试，便于继续补齐玩法 |

游戏逻辑与资产事务由本仓库维护。玩家登录器、更新分发和 GM 管理界面可以通过独立工具接入。

## 登录器与 GM

推荐使用 **[PurpleMagic](https://github.com/Cec1c/PurpleMagic)**，它提供 Windows 玩家登录器、更新网关和 Web GM，覆盖注册登录、客户端更新、账号角色查询、背包、邮件和福利管理。

**最基础的登录器，直接跟 Agent 许愿即可。PurpleMagic 是推荐方案，并非唯一指定要求。** 可以先实现注册、登录、选频道和启动游戏，再按需要增加公告、自动更新或自己的界面。

- [PurpleMagic 中文介绍](https://github.com/Cec1c/PurpleMagic/blob/main/README.zh-CN.md)
- [网关与 GM 部署](https://github.com/Cec1c/PurpleMagic/blob/main/docs/DEPLOYMENT.zh-CN.md)
- [生成自己的登录器](https://github.com/Cec1c/PurpleMagic/blob/main/docs/LAUNCHER_INPUTS.md)

## 自己构建

安装 [Go 1.26.5](https://go.dev/dl/)，在仓库根目录执行：

**Windows：**

```bat
.\build.bat
.\build.bat linux amd64
```

**Linux / macOS：**

```sh
sh build.sh
sh build.sh linux amd64
```

程序输出到 `dist/<GOOS>-<GOARCH>/`：

| 程序 | 用途 |
| --- | --- |
| `DNF90Server` | 游戏服务 |
| `DNF90Control` | 匹配本地实例的部署控制器 |
| `DNF90Doctor` | 环境与配置预检 |

Windows 产物带 `.exe` 后缀。首次构建需要下载 Go 模块；已有依赖缓存时可离线构建。运行配置、数据库和资源的准备步骤见[部署说明](Docs/BUILD_AND_DEPLOY.md)。

## 开发与文档

| 要做的事 | 入口 |
| --- | --- |
| 第一次构建、开服或让 Agent 帮忙 | [大肥鱼也能看得明白的构建与部署说明](Docs/BUILD_AND_DEPLOY.md) |
| 理解代码职责与协作规则 | [AGENTS.md](AGENTS.md) |
| 查看近期改动 | [changlog.md](changlog.md) |
| 追踪客户端协议与函数 | [客户端函数索引](Docs/CLIENT_FUNCTION_INDEX.md) |
| 研究任务和玩法问题 | [Seeker 工作流](Docs/SEEKER_WORKFLOW.md) · [调查与修复方案](Docs/seeker/README.md) |
| 提交修复、了解镜像关系 | [仓库协作与同步](Docs/REPOSITORY_SYNC.md) |
| 查阅早期导入与验收记录 | [源码导入](Docs/SOURCE_IMPORT.md) · [D33 修复](Docs/D33_REPAIR_STATUS.md) · [验收交接](Docs/ACCEPTANCE_HANDOFF.md) |

```text
go-server/cmd/server/                   服务端、控制器与检查工具
go-server/internal/app/dnf90/           应用组装
go-server/internal/services/dnfbridge/  网络会话与客户端协议
go-server/internal/modules/dnf/         游戏领域规则与事务
go-server/internal/services/logic/dnf/  仓储组装
deploy/                                部署工具与配置模板
Docs/                                  使用说明、协议索引与修复方案
runtime/                               私有配置、资源、日志与运行数据
dist/                                  构建产物
```

测试命令与数据库测试准备见[构建与部署说明](Docs/BUILD_AND_DEPLOY.md#改了源码怎么检查)。运行数据保存在 Git 忽略目录，源码变更使用独立分支提交。

## 上游与贡献

[16ww/servers5a3](https://gitgud.io/16ww/servers5a3) 是 GitGud 上游，当前 GitHub 仓库 [Cec1c/ServerS5A3](https://github.com/Cec1c/ServerS5A3) 镜像其主干。修复通过上游 Merge Request 集成，再同步到 GitHub。

感谢上游作者和各位贡献者持续完善协议与玩法。根目录构建脚本及独立产物目录的组织参考了 [rewio/ServerS4A21](https://gitgud.io/rewio/ServerS4A21)。

## 许可证

本项目采用 **GNU General Public License v3.0（GPL-3.0-only）**，完整条款见 [LICENSE](LICENSE)。第三方组件保留各自的许可证与版权声明。
