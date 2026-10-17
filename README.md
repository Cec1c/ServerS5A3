## 服务端与 GM 工具的仓库边界

本在线仓库维护游戏服务器。GM 工具作为独立系统在本地维护，其源码、界面、启动与更新不进入本仓库。服务端不再内嵌启动 GM Web 面板，也不读取 GM 监听、密码或数据库配置来创建面板。旧实例文件中的 `server.gmListen` 可兼容读取，但非空时会明确要求先迁移独立 GM 并移除该设置；读取不会改写原文件。

本地已有 GM 工具、源码与运行文件应保留。游戏本身使用的域规则、账号接口、邮件投递和服务器管理接口继续由服务端维护。历史提交保留，本次以新提交移除当前版本中的 GM 工具内容。

# ServerS5A3 / DNF90 Go 服务端

DNF90 的 Go 服务端源码，包含游戏服务、部署控制器、环境检查工具、领域模块、协议实现和测试。可构建 Windows、Linux 和 macOS 程序。

源码基于 2026-10-11 导入的 D22/D23 候选版本，历史基线共 3,356 个文件；后续修复见 [D33 修复与验证](Docs/D33_REPAIR_STATUS.md)。

> [!IMPORTANT]
> `master` 包含候选修复源码，线上版本仍为 r98。游戏实机验收进度见[源码状态](Docs/SOURCE_IMPORT.md)和[验收交接](Docs/ACCEPTANCE_HANDOFF.md)。部署前先核对目标客户端与已验收项目。

## 独立 GM

管理工具已迁往 **[Cec1c/DNF90GM](https://github.com/Cec1c/DNF90GM)**，在独立仓库构建、发布和部署。
游戏事务和私有管理协议由本仓维护；GM 页面、福利扫描与批量发放由独立 GM 提供。
现有部署的兼容与迁移入口见 [GM 仓库迁移](Docs/GM_STANDALONE.md)。

## 构建

安装 **Go 1.26.5**，将 `go` 加入 PATH。首次构建会下载 `go.mod` 声明的依赖；依赖已缓存时可离线构建。

Windows在仓库根目录运行：

```bat
build.bat
build.bat linux amd64
```

Linux/macOS：

```sh
sh build.sh
sh build.sh windows amd64
sh build.sh linux amd64
```

脚本将 `DNF90Server`、`DNF90Control` 和 `DNF90Doctor` 写入 `dist/<GOOS>-<GOARCH>/`。Windows 默认构建 `windows/amd64`，shell 默认使用当前平台；通过 `GO_EXE` 可指定 Go 编译器。构建失败返回非零状态，运行服务按下方步骤单独准备。

直接构建服务端：

```sh
cd go-server
go build -buildvcs=false -mod=readonly -trimpath -o ../dist/DNF90Server ./cmd/server/dnf90
```

根目录构建入口和独立产物目录的组织方式参考 [rewio/ServerS4A21](https://gitgud.io/rewio/ServerS4A21)；本仓采用 Go 模块构建。

## 测试

```sh
cd go-server
go list -buildvcs=false -mod=readonly ./...
go test -buildvcs=false -mod=readonly -count=1 ./internal/modules/dnf/...
go test -buildvcs=false -mod=readonly -count=1 ./internal/services/dnfbridge
go test -buildvcs=false -mod=readonly -count=1 ./cmd/server/control
go vet -mod=readonly ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
```

数据库、客户端及专用资源测试需要对应环境，实机验收单独记录。GitLab CI 配置了 Linux/Windows amd64 构建、上述测试和 SAST；执行结果见仓库流水线。

## 运行准备

运行时需自行准备匹配的客户端、`Script.pvf`、`channel_info.etc`、数据库、凭据和实例配置，并使用上一步生成的程序。

`deploy/templates/instance.example.json` 适用于旧 `90cn-decode-bypass-v1` 控制器及 portable MySQL/hook 客户端环境。复制后填写实际数据库密码。D32 MariaDB/native 实例需要对应的部署方案，不能使用该旧控制器直接接管。

```text
runtime/bin/                   编译后程序
runtime/config/instance.json   私有实例配置
runtime/configs/               生成配置
runtime/data/dnf/              PVF和通道文件
runtime/logs/                  日志
```

在匹配环境中使用 `deploy/windows/control.bat` 管理实例。bootstrap、admin 和 MySQL 绑定 loopback；game 和 UDP 使用受支持的私有 LAN 地址。

## 目录与协作

```text
go-server/cmd/server/                    服务端及工具入口
go-server/internal/app/dnf90/            单一组装入口
go-server/internal/services/dnfbridge/   传输、会话和协议兼容
go-server/internal/modules/dnf/          领域规则和事务
go-server/internal/services/logic/dnf/   仓储组装
go-server/internal/platform/             框架闭包
go-server/pkg/                           公共包
deploy/                                 示例配置和BAT入口
Docs/                                   导入状态和逐文件清单
```

协作前阅读 [AGENTS.md](AGENTS.md) 和 [changlog.md](changlog.md)。源码放在 `go-server/`；私有运行配置、密码、日志、数据库、PVF 和构建产物保存在忽略目录中。
