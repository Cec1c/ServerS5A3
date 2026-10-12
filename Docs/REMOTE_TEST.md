# 90cn 远端测试

这套环境使用腾讯云 Ubuntu、Docker 和 MariaDB 11.8（MySQL 协议），游戏流量经过 WireGuard 私网。日常入口是仓库根目录的 `START_REMOTE.bat`；`START_LOCAL.bat` 仍连接本机 Docker。两边的角色数据各自保存，初次部署时从本地一致性快照导入远端，以后不会自动合并角色进度。

## 本机使用

1. 确认 WireGuard 的 `90cn` 隧道处于运行状态；首次安装已注册 Windows 自动启动服务。
2. 双击 `START_REMOTE.bat`，使用已有测试账号登录，或注册新账号。
3. 启动器校验客户端和远端 PVF 指纹，取得登录令牌，再启动原版 `DNF.exe`。关闭原客户端后才能启动另一份。
4. 游戏退出后关闭登录窗口，SSH 转发随之结束。关闭登录窗口时如果游戏仍在运行，转发会保留到游戏退出。

无需本机 Docker 运行。不要使用客户端自带更新器更新这份匹配资源。客户端路径、SSH 别名和本地转发端口保存在忽略提交的 `runtime/remote/client.json`；远端资产指纹每次启动时经 SSH 下载。私钥和配置留在权限受限的 `runtime/remote`，不可上传到仓库。

## 地址与端口

| 用途 | 地址 |
| --- | --- |
| Windows WireGuard | `10.90.0.2/32`，只路由 `10.90.0.1/32` |
| 远端 WireGuard | `10.90.0.1`，公网只承载 UDP 51820 隧道 |
| 引导目录 | 本机 `127.0.0.1:27001` → SSH → 远端 `127.0.0.1:7001` |
| 账号服务 | 本机 `127.0.0.1:27002` → SSH → 远端 `127.0.0.1:17002` |
| 游戏 TCP | 仅绑定 `10.90.0.1`，118 个真实频道端口见 `runtime/docker/data/manifest.json` |
| 组队 UDP | `10.90.0.1:30000–30063`，内置 relay |
| 管理服务 | 远端 `127.0.0.1:18111` |
| MariaDB | Docker 内网 `10.254.90.3:3306`，无宿主机发布端口 |

云安全组和宿主机防火墙预留 TCP 22、80、443、10000–10999，以及 UDP 51820、30000–31999。预留规则不等于有服务监听；游戏端口当前只在私网上监听。3306、7001、17002、18111 不应开放公网。

## 服务器维护

SSH 别名为 `90cn`，使用 `ubuntu` 和免密 sudo。项目目录 `/opt/servers5a3`。

```sh
ssh 90cn
sudo systemctl status servers5a3
cd /opt/servers5a3
docker compose -p servers5a3-remote -f runtime/docker/compose.json ps
sudo systemctl restart servers5a3
python3 deploy/remote/backup.py
```

`runtime/docker/instance.json` 是远端配置源；修改后重启 `servers5a3` 重新生成配置。不要编辑 `compose.json`、`server.env` 或 `server/configs`。服务镜像固定到远端加载后的 image ID，数据库卷为 `servers5a3-remote_database`。日常停止用 `systemctl stop servers5a3`，禁止用 `down -v` 清数据库。

服务端内存上限 3 GiB，Go 软限制 2500 MiB，数据库上限 384 MiB；宿主机另有 2 GiB swap。这个容量适合当前少量测试账号，不代表多人并发容量已经验证。

`servers5a3-backup.timer` 每天北京时间 04:00 生成一致性 SQL 压缩备份到 `runtime/backups`，失败文件保留 `.partial`。目前备份保存在同一台服务器，尚未配置异地备份和自动清理；重装或迁移前先下载备份。首次迁移的本地快照也保留在本机 `runtime/remote/local-database.sql`。

## 重建顺序

1. 安装 Docker、Compose v2、Python 3、WireGuard；配置服务端 `wg90cn` 和客户端 `90cn`，密钥分别在所属机器生成。客户端只设置服务器 `/32` 路由。
2. 将审核过的源码和匹配 PVF/频道表放到 `/opt/servers5a3`。沿用 `deploy/docker/manage.py` 生成配置；远端使用独立的私网、随机数据库密码和管理令牌。参考本机已保存的配置，不要把密码粘贴到命令行或文档。
3. 本机通过 `deploy/docker/Dockerfile` 构建 Linux 镜像。远端 Docker Hub 访问不稳定时，用 `docker save`、压缩、SCP、校验 SHA256、`docker load`；将加载后的实际镜像 ID 写入配置。Docker 的不同存储实现可能显示不同层级的 ID，传输归档 SHA256 应严格一致。
4. 先 `prepare --project servers5a3-remote`，再只启动数据库。只有首次创建的空数据库才导入选定快照，禁止覆盖已有测试进度。
5. `sudo sh deploy/remote/install.sh` 安装服务和每日备份；`sudo systemctl start servers5a3` 启动。WireGuard 先于 Docker，游戏服务等待数据库健康。
6. 核实私网端口、账号令牌、角色持久化和真实客户端玩法；保留回退镜像及数据库备份。

## 验证边界

- 已验证实际宿主机重启后 VPN、数据库、118 个频道自动恢复；客户端通过私网连接普通频道 10，使用令牌绑定原账号，读取迁移的角色。
- 实测原角色进入洛兰教程，键盘移动、击杀怪物（远端确认死亡并发放 39 经验），随后进入频道 10 的艾尔文防线城镇。按用户要求在连接成立后停止界面操作；没有继续进行完整游戏流程或多人验收。
- 初次远端登录卡住的原因是实测 610 字节登录体进入旧协议忽略路径。补充精确长度识别；原有 606 字节和相邻长度拒绝测试保留。四字节差异的具体客户端字段原因尚未确定。
- Go list、domain/bridge/control 测试、指定 vet、Windows 编译和 Linux Docker 构建通过；4 项部署边界测试通过。
- 旧 Windows 原生控制器的 `check --skip-database` 仍因缺少 `deploy/assets/channel_info.etc` 失败；远端 Docker 使用已明确选择并记录指纹的 118 频道参考表，不依赖这个旧路径。
- PVF 中的两个单人团本仍存在缺失地图引用（5314→292101788、5323→95267）。组队、完整任务链和团本需要单独验收；本次连接验证不代表这些玩法都已通过。
