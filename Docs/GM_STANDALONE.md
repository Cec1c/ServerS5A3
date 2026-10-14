# 独立 GM 服务

GM 运行在独立的 `DNF90GM` 进程／镜像中，拥有自己的 HTTP 服务、数据库连接池、PVF 只读副本、物品索引和福利扫描任务。游戏服务不启动 Web GM、不嵌入 GM 页面，也不在选角进城时执行物品福利规则。

GM 复用本仓的 Go 领域／仓储事务库。共享业务库不要求共享运行进程；没有复制另一套余额、角色或邮件规则。MariaDB 适配继续使用当前库名、表结构和账号 ID，不将参考工具的 SQLite 表直接导入。

## 数据与接口

| 能力 | 所属位置 |
| --- | --- |
| 管理页面、鉴权、PVF 查询、批量任务、福利规则和自动扫描 | 独立 GM |
| 角色／账号查询、邮件事务及持久发放回执 | GM 通过现有 MariaDB 仓储事务访问 |
| 实时在线状态、活动运行状态、运行时商品能力、客户端行编码 | 游戏服私有 `/api/game/admin` 接口 |
| 登录、角色初始化、玩家实际领取邮件 | 游戏服务 |

新账号初始点券／新角色初始金币仍是游戏注册、角色创建事务中的初始化规则；GM 只管理其配置。这与“物品福利扫描器挂在游戏登录流程中”不同。

私有游戏接口使用既有管理员密钥，不进入玩家公开网关白名单。GM 对相关离线操作查询失败时拒绝写入，不把未知当作离线。活动开关是否需要游戏重启，由游戏实际运行状态返回；重启 GM 不会改变活动的生效状态。

普通邮件发物品在独立 GM 中也采用数据库事务回执。两个 GM 连接池并发、HTTP 回包丢失、邮件删除和 GM 重启后，相同请求不会再次投递。福利使用“规则 + 角色及创建时间”去重；扫描游标只是进度，重启从头扫描不会清空已发记录。

## 构建与配置

在 `go-server` 内构建：

```text
go build -buildvcs=false -o ../runtime/bin/DNF90GM.exe ./cmd/gmtool
```

旧 Windows 本地控制器的 `server.gmListen` 不再启动内嵌 GM；Windows 使用上述独立程序和专用配置，游戏私有管理接口的密钥通过 `DNFBRIDGE_PLAYER_ADMIN_SECRET` 配置。

独立配置示例见 `deploy/templates/gm.example.json`。配置须只对部署用户可读，使用已有受限 MariaDB 凭据、明确的库名和当前 PVF SHA256。GM 不自动创建／迁移游戏表。

```text
DNF90GM --config <protected-gm.json> --check
DNF90GM --config <protected-gm.json> --init-schema
```

`--check` 只核对配置格式，不连接数据库或启动 HTTP。`--init-schema` 允许初始化 GM 福利附加表，然后启动服务；不会创建规则、启用福利或发奖。规则必须由管理员单独启用。

## Docker

`instance.json` 保留 `gm_enabled`、`gm_password`、`gm_port`，可指定 `gm_image`、`gm_ip`、`gm_memory_limit` 和 `gm_go_memory_limit`（Go 软内存上限默认 `768MiB`）。生成器将其生成到独立的 `gm` 服务和受保护的 `gm/config.json`；游戏环境文件不再包含 GM 密码或 GM 监听地址。GM 仅向宿主 loopback 映射管理端口，由既有管理网关提供外部鉴权入口。

```text
python deploy/docker/manage.py build-gm --gm-image <gm-image-tag>
python deploy/docker/manage.py prepare --runtime-dir <runtime-directory>
python deploy/docker/manage.py up-gm --runtime-dir <runtime-directory> --project <compose-project>
```

生成前把相同镜像标签写入源 `instance.json` 的 `gm_image`。`up-gm` 只重建 `gm`，使用 `--no-deps`，不会重启游戏或数据库。

首次从内嵌版迁移时，需先备份配置／数据库，基于实际累计修复版本构建带私有管理接口的游戏服，并在维护时移交原 GM 端口。保留原镜像和配置供回退；不回退玩家数据库。此后 GM 页面、福利及批量任务更新只需要部署 GM。

## 参考来源

研究参考 [Liuxiny/86jp_DfoGmTool](https://github.com/Liuxiny/86jp_DfoGmTool) 的独立进程、自带业务库、读取数据库与 PVF 的模式。参考快照为 `13a45286166643f9e0f7bfce81c9ac67134dd848`；其 SQLite schema v26、86JP 业务契约不能直接代替当前 90CN MariaDB 契约。本次复用本仓 Go 实现，没有将其 C# 代码或运行数据复制进来。
