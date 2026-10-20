# 大肥鱼也能看得明白的构建与部署说明

[返回项目首页](../README.md)

你准备服务器、客户端和想法，Agent 负责读源码、运行命令、配置服务和排查问题。第一步的目标很具体：让自己的账号能进入游戏，创建角色，打一张图，退出后还能读到存档。

## 先认识这几样东西

| 名词 | 可以这样理解 |
| --- | --- |
| 源码 | 这个仓库里的程序代码，Agent 会从这里构建服务端 |
| 构建 | 把源码变成 `DNF90Server` 等可执行程序 |
| 部署 | 把程序、配置、数据库和资源放到目标机器并启动 |
| 客户端 | Windows 上运行的 `DNF.exe` 及其资源目录 |
| PVF | `Script.pvf`，保存任务、物品、地图等游戏定义 |
| 登录器 | 注册和登录账号、检查频道，再把连接信息交给游戏客户端 |
| GM | 管理账号、角色、物品、邮件和福利的工具 |

## Agent 从哪开始用

选择能打开本地文件夹、编辑文件并运行终端命令的编程 Agent。需要远程部署时，再给它 SSH 连接能力。以项目目录作为工作区，让它先读 `AGENTS.md`，就能知道代码放在哪里、怎样构建、哪些步骤需要验证。

可以通过 Git 下载项目：

```sh
git clone https://github.com/Cec1c/ServerS5A3.git
cd ServerS5A3
```

也可以在 GitHub 页面选择 **Code → Download ZIP**，解压后让 Agent 打开这个文件夹。需要持续更新和提交修改时，使用 Git 更方便。上游地址是 <https://gitgud.io/16ww/servers5a3>。

对 Agent 说清楚希望得到什么结果即可。它缺少服务器地址、资源路径或使用范围时，你再补这些信息；编译报错和配置排查可以继续交给它处理。

## 先把东西备齐

| 准备项 | 需要告诉 Agent 的信息 |
| --- | --- |
| Linux 服务器 | IP、SSH 端口、用户名、密码或密钥，以及可用的管理权限 |
| 使用范围 | 自己玩、局域网朋友一起玩，还是通过公网连接 |
| Windows 客户端 | 客户端完整目录，包含 `DNF.exe`、`Script.pvf` 及对应的 dstr/NPK 等资源 |
| 配套频道表 | 从当前 PVF 提取，或提供已选定的外部 `channel_info.etc` |
| 已有数据 | 告诉它这是全新安装，还是要保留已有账号、角色和配置 |
| 配套工具 | 要一个基础登录器，还是接入 PurpleMagic 的登录器、更新和 GM |
| 域名 | 公网 HTTPS 网关使用的域名；有的话一并提供 |

密码和密钥通过你使用的 Agent 的私有凭据方式提供，保存在本地受限配置中。公开仓库、Issue 和部署报告只记录连接方式与配置路径。

当前 Docker 配置将游戏容器的内存上限设为 4 GiB，数据库和系统还需要额外空间。让 Agent 根据 PVF、在线人数和实际进程占用安排容量。

### PVF 和 EXE 在哪里下载

**`Script.pvf` 和对应版本的 `DNF.exe` 都挂在 Release 附件里：**

- [GitHub Releases](https://github.com/Cec1c/ServerS5A3/releases)
- [GitGud 上游 Releases](https://gitgud.io/16ww/servers5a3/-/releases)

选定一个版本，阅读说明，下载该版本配套的 EXE、PVF 和列出的其他资源；分卷包按说明下载全部分卷后解压到客户端目录。把最终客户端目录交给 Agent。

> [!IMPORTANT]
> 先固定一套客户端、PVF、频道表和协议配置。Agent 应记录完整 SHA256，并核对玩家、游戏服务和 GM 的配套关系。

## 直接复制这段话开始许愿

把方括号里的内容换成自己的信息，再发给 Agent：

```text
请帮我部署这个 ServerS5A3 / DNF90 项目。

先阅读 AGENTS.md、README.md、Docs/BUILD_AND_DEPLOY.md 和 changlog.md。

我的环境：
- 服务器：[IP 或 SSH 别名]
- SSH 端口与用户名：[端口、用户名]
- 认证：[使用已配置的 SSH 密钥，或通过私有方式提供密码]
- Windows 客户端：[完整目录]
- 使用范围：[自己玩 / 局域网 / 公网]
- 数据处理：[全新安装 / 保留现有账号和角色]
- 域名：[已有域名；没有则说明]
- 登录器与 GM：[基础登录器 / PurpleMagic / 自己指定的工具]

请先核对目标机器、源码版本、现有进程、数据库和客户端配套。
按当前源码支持的方式构建，生成独立运行配置，准备数据库和资源。
有旧数据时先备份并验证，再部署本次服务。

请完成账号注册、登录令牌、频道连接和存档持久化检查，
给我一个可用的登录器，并列出需要我在游戏内验证的步骤。
最后留下服务目录、版本摘要、启动停止、日志、备份、更新和回退方法。
```

还可以继续许愿：“服务器叫这个名字”“登录器用这张背景”“帮我加公告”“给朋友做个下载包”。把目标、适用对象和期望效果说清楚，Agent 就能按项目实际接口拆解实现。

## 自己构建服务端

源码构建需要 Git 和 **[Go 1.26.5](https://go.dev/dl/)**。版本来自 [`go-server/go.mod`](../go-server/go.mod)。先确认终端能找到 Go：

```sh
go version
```

在仓库根目录执行以下命令。

### Windows

```bat
.\build.bat
```

产物放在 `dist/windows-amd64/`。要为 Linux 服务器构建：

```bat
.\build.bat linux amd64
```

### Linux 或 macOS

```sh
sh build.sh
```

默认构建当前系统和架构。也可以明确指定目标：

```sh
sh build.sh linux amd64
sh build.sh windows amd64
```

两个脚本都生成 `DNF90Server`、`DNF90Control` 和 `DNF90Doctor`，输出目录为 `dist/<GOOS>-<GOARCH>/`，Windows 文件带 `.exe`。通过环境变量 `GO_EXE` 可以指定 Go 编译器路径。

只需要游戏服务程序时，在 `go-server/` 目录执行：

```sh
go build -buildvcs=false -mod=readonly -trimpath -o ../dist/DNF90Server ./cmd/server/dnf90
```

到这里得到的是程序文件。下一步准备运行配置、数据库和资源，再启动服务。

## 在 Linux 上启动一套测试服

下面使用仓库现有的 Docker 私网配置，适合局域网或通过 VPN 连通的测试环境。服务器准备 **Docker Engine、Compose v2 和 Python 3.10+**；游戏客户端运行在 Windows。Dockerfile 会在镜像中准备 Go 构建环境。

在 Linux 服务器的仓库根目录执行。示例的 `/srv/dnf-assets/Script.pvf` 替换为上传后的真实 PVF 路径，`192.168.1.50` 替换为**这台服务器网卡上实际存在、玩家可达的私网 IPv4**：

```sh
sh deploy/docker/manage.sh build
sh deploy/docker/manage.sh prepare --pvf "/srv/dnf-assets/Script.pvf" --host-ip 192.168.1.50
```

`prepare` 会生成独立数据库密码和管理令牌，保存 PVF，提取频道表，并生成 Compose、环境文件和服务端配置。成功时会输出真实频道数量和 `instance.json` 路径。

如果采用已经确认配套的外部频道表，在启动前明确指定：

```sh
sh deploy/docker/manage.sh prepare --channel-info "/srv/dnf-assets/channel_info.etc"
```

启动并检查：

```sh
sh deploy/docker/manage.sh up
sh deploy/docker/manage.sh status
sh deploy/docker/manage.sh logs
curl http://127.0.0.1:17002/api/status
```

`up` 会等待数据库和游戏服务的健康检查。确认服务状态后，继续接入登录器并进行游戏内验证。

### 配置和存档放在哪里

| 位置 | 内容 |
| --- | --- |
| `runtime/docker/instance.json` | 实例配置源，后续配置修改从这里开始 |
| `runtime/docker/data/` | PVF、频道表和记录资源摘要的 `manifest.json` |
| `runtime/docker/server/configs/` | 生成的服务配置 |
| `runtime/docker/server/logs/` | 游戏服务日志 |
| `runtime/docker/compose.json`、`*.env` | 生成的容器与环境配置 |
| Docker 的 `servers5a3-local_database` 卷 | 默认项目的数据库存档 |

修改 `instance.json` 后使用 `prepare` 或 `up` 重新生成配置。数据库卷随容器停止保留；更换项目名、运行目录或迁移机器时，让 Agent 一并确认存档位置。

### 玩家怎么连进来

| 入口 | 当前 Docker 配置 |
| --- | --- |
| 频道引导 TCP 7001 | 宿主机 `127.0.0.1` |
| 账号 API TCP 17002 | 宿主机 `127.0.0.1` |
| 管理 TCP 18111 | 宿主机 `127.0.0.1`，使用管理令牌 |
| 游戏频道 TCP | 指定私网 IP，端口由真实频道表生成 |
| 组队 UDP 30000–30063 | 指定私网 IP |
| MariaDB | Docker 私有网络 |

远端 Windows 玩家需要能访问游戏和 UDP 的私网地址；频道引导与账号 API 可通过 SSH 转发或配套网关接入。让 Agent 按这张表检查玩家到每个入口的实际路径，再生成登录器配置。

公网部署可以使用 PurpleMagic 的 HTTPS 玩家网关，再单独配置游戏引导、频道 TCP 与组队 UDP。当前仓库的 `manage.py` 接受私网地址；公网布局需要 Agent 按目标网络配置监听、公告地址和鉴权，并验证实际游戏会话绑定到登录账号。相关接口见 [PurpleMagic 网关接入](https://github.com/Cec1c/PurpleMagic/blob/main/docs/GATEWAY_INTEGRATION.md)。

### Windows 本机测试

安装 Docker Desktop 和 Python 后，可在仓库根目录使用同一套配置生成器：

```bat
.\DOCKER.bat build
.\DOCKER.bat prepare --pvf "D:\GameClient\Script.pvf" --client-dir "D:\GameClient" --host-ip 192.168.1.50
.\DOCKER.bat up
.\DOCKER.bat status
```

将地址替换为 Windows 主机的实际私网地址。登录器按下节准备。仓库旧 `LOCAL_CLIENT.bat` 对 EXE 有固定版本摘要校验，Agent 应先核对匹配情况；[`deploy/templates/instance.example.json`](../deploy/templates/instance.example.json) 和 `deploy/windows/control.bat` 则用于原 portable MySQL / hook 客户端配置，与 Docker 实例分开管理。

## 登录器和 GM 怎么选

**最基础的登录器，直接跟 Agent 许愿即可。** 可以把需求写成：

```text
请根据这套服务端和我提供的客户端，实现一个 Windows 基础登录器。
需要注册、登录、选择频道和启动游戏。
登录时使用服务端实际返回的令牌，核对客户端与 PVF 配套，
验证进入游戏后绑定的是当前账号，并把连接配置单独保存。
```

希望直接使用完整工具链时，推荐 **[PurpleMagic](https://github.com/Cec1c/PurpleMagic)**。它提供玩家登录器、更新网关和 Web GM。**这是推荐方案，并非唯一指定要求**；你可以使用符合当前登录协议的其他登录器，或让 Agent 做自己的版本。

| 需求 | 文档 |
| --- | --- |
| 看工具能力与界面 | [PurpleMagic 中文 README](https://github.com/Cec1c/PurpleMagic/blob/main/README.zh-CN.md) |
| 部署网关、GM 和 PVF 索引 | [中文部署说明](https://github.com/Cec1c/PurpleMagic/blob/main/docs/DEPLOYMENT.zh-CN.md) |
| 对接账号、游戏和管理接口 | [网关接入契约](https://github.com/Cec1c/PurpleMagic/blob/main/docs/GATEWAY_INTEGRATION.md) |
| 构建自己的玩家登录器 | [登录器输入与发布](https://github.com/Cec1c/PurpleMagic/blob/main/docs/LAUNCHER_INPUTS.md) |

先把游戏服务和 MariaDB 启动，再按 PurpleMagic 文档配置连接、TLS、签名与同代 PVF 索引。客户端目录、服务器地址和更新源由自己的环境提供。

## 怎么知道已经搭好了

请 Agent 交付服务状态、源码提交、程序和资源摘要，以及实际使用的端口与配置路径。然后用 Windows 客户端走完这组步骤：

1. 注册新账号，登录并看到正确的频道。
2. 创建角色，进入城镇，移动并与 NPC 交互。
3. 接一个任务，进入对应副本，完成战斗与结算。
4. 返回城镇，检查经验、物品和任务进度。
5. 退出游戏后重新登录，确认角色与存档保留。
6. 准备多人游戏时，再用两个独立账号检查身份、组队、进图和同步。

遇到问题，把操作步骤、发生时间、频道、副本和截图交给 Agent，让它与日志、报文及当前资源版本对照。自动测试结果和这份游戏内清单分别记录，后续修复就有准确的起点。

## 停服、备份和更新

Docker 测试实例的常用命令，在仓库根目录执行：

```sh
sh deploy/docker/manage.sh status
sh deploy/docker/manage.sh logs
sh deploy/docker/manage.sh down
sh deploy/docker/manage.sh up
```

`down` 停止并移除本项目容器和网络，保留数据库卷。平时把状态、日志、启停各自作为一次操作执行。

更新前，让 Agent 保存数据库一致性备份、实例配置、当前镜像或程序，以及客户端资源摘要；将备份恢复到独立测试库验证后，再切换服务。已有部署以实际运行版本为基线，保留自己的配置与功能修改。

如果某项服务端修复依赖新的 `DNF.exe`，先让玩家更新源提供已验证的配套客户端，再更新对应服务端。更新完成后检查实际运行程序和下载文件，最后重走上面的游戏内步骤。

让 Agent 把本机的启动、停止、日志、定时备份和恢复命令写成一份私有运维记录。回退程序时保留最新玩家存档；数据库恢复作为独立的数据操作处理。

## 改了源码怎么检查

以下命令在 `go-server/` 目录运行：

```sh
go list -buildvcs=false -mod=readonly ./...
go test -buildvcs=false -mod=readonly -count=1 ./internal/modules/dnf/...
go test -buildvcs=false -mod=readonly -count=1 ./internal/services/dnfbridge
go test -buildvcs=false -mod=readonly -count=1 ./cmd/server/control
go vet -mod=readonly ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
```

修改 Go 文件后先运行 `gofmt`，检查通过后再用根目录构建脚本生成目标平台程序。数据库测试变量 `DNFBRIDGE_PLAYER_TEST_DSN` 指向独立的一次性测试库；依赖客户端或 PVF 的测试使用已核对的资源。

修改 Docker 工具时，在仓库根目录运行：

```sh
python3 -m unittest discover -s deploy/docker -p "test_*.py" -v
```

贡献变更前阅读 [AGENTS.md](../AGENTS.md) 和[仓库协作流程](REPOSITORY_SYNC.md)。代码和文档通过 GitGud 上游 MR 集成，GitHub 随后镜像主干；每项修复说明触发条件、修改内容和验证结果。
