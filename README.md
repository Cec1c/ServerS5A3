# ServerS5A3 / DNF90 Go 服务端

DNF90 服务端Go源码仓库，包含服务端、部署控制器、部署检查工具、领域模块、协议实现和测试。

本次导入为2026-10-11的D22/D23候选源码，共3,356个源文件，与本地冻结源码逐文件一致。线上仍使用r98，候选修复尚未完成全部游戏实机验收，详见[源码状态](Docs/SOURCE_IMPORT.md)。

## 构建

安装 **Go 1.26.5**，确保`go`在PATH中。首次构建需要下载go.mod指定模块，已有模块缓存可离线构建。

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

构建`DNF90Server`、`DNF90Control`和`DNF90Doctor`，输出到`dist/<GOOS>-<GOARCH>/`，失败立即返回非零状态。Windows默认windows/amd64，shell默认当前平台；可用`GO_EXE`指定Go编译器。构建只产生文件，不启动服务或访问数据库。

直接构建服务端：

```sh
cd go-server
go build -buildvcs=false -mod=readonly -trimpath -o ../dist/DNF90Server ./cmd/server/dnf90
```

构建组织参考[rewio/ServerS4A21](https://gitgud.io/rewio/ServerS4A21)的根目录脚本和独立发布目录。参考项目为.NET，本仓库按Go模块构建，未复制其实现。

## 测试

```sh
cd go-server
go list -buildvcs=false -mod=readonly ./...
go test -buildvcs=false -mod=readonly -count=1 ./internal/modules/dnf/...
go test -buildvcs=false -mod=readonly -count=1 ./internal/services/dnfbridge
go test -buildvcs=false -mod=readonly -count=1 ./cmd/server/control
go vet -mod=readonly ./cmd/server/control ./cmd/server/dnf90 ./cmd/server/doctor ./internal/app/dnf90 ./internal/services/dnfbridge
```

数据库、客户端或专用资源测试有独立启用条件，跳过不能视为现场验收通过。GitLab CI提供Linux/Windows amd64构建、上述测试和原有SAST模板，实际CI结果以仓库流水线为准。

## 运行准备

构建后仍需准备匹配客户端、`Script.pvf`、`channel_info.etc`、数据库和实例配置。仓库不包含运行资产、玩家数据、凭据或预编译程序。

`deploy/templates/instance.example.json`为旧`90cn-decode-bypass-v1`控制器示例，数据库密码为占位符，使用前必须修改。该控制器只支持原portable MySQL/hook客户端环境，不能直接接管D32 MariaDB/native实例。仅下载源码不能保证直接运行或适配任意客户端。

```text
runtime/bin/                   编译后程序
runtime/config/instance.json   私有实例配置
runtime/configs/               生成配置
runtime/data/dnf/              PVF和通道文件
runtime/logs/                  日志
```

已有匹配环境可使用`deploy/windows/control.bat`。bootstrap/admin/MySQL保持loopback，game和UDP按受支持私有LAN配置。构建脚本不自动部署。

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

协作前阅读[AGENTS.md](AGENTS.md)和[changlog.md](changlog.md)。源码模块保持source-only，运行配置、密码、日志、数据库、PVF及构建产物不得提交。
