# 旧版 Docker 纯 NAT 的 IPv4 回环发布保护

`deploy/docker/localhost_publish_guard.py` 管理一条带固定标识的 IPv4 raw PREROUTING 规则，拒绝从非回环接口进入、目的地址属于 `127.0.0.0/8` 的报文。宿主机自身的回环访问继续走本地路径。规则用于保持旧版 Docker 从用户态代理切换到内核 NAT 时的回环发布边界。

Docker 官方记录了 28.0.0 之前版本中，部分二层网络访问能够到达回环发布端口的行为。具体适用性应按实际版本、网络与防火墙验证。[Docker 端口发布文档](https://docs.docker.com/engine/network/port-publishing/)

## 工具合同

- `sudo python3 deploy/docker/localhost_publish_guard.py --check` 读取当前规则及固定策略摘要。
- `sudo python3 deploy/docker/localhost_publish_guard.py` 在进程锁内检查并按需插入自有规则。重复调用保留同一条规则；其他规则保持原有内容和顺序。
- 同标识但策略不符、重复自有规则、位于其他 PREROUTING 规则之后、命令异常及不安全锁文件都会中止。工具通过固定参数执行 iptables，保留 xtables 锁等待，并限制子进程运行时间。
- 发布协调器保存启用前的规则存在状态与策略摘要；回退时通过 `remove_if_created` 保留预先存在的规则，只移除本次创建的同一策略。

工具的进程锁保存在 `/run/servers5a3-localhost-publish-guard.lock`。保留锁文件 inode，以便并发调用始终使用同一把锁。

## 与发布协调器配合

正式维护须封存 Docker 配置、启动策略、数据库及其他容器身份、端口绑定、桥接接口与 `route_localnet` 原值。协调器负责启动前保护、配置持久化、维护授权、程序切换及实际验证；IPv6 发布需要独立适用性验证。

`deploy/docker/localhost_publish_persistence.py` 提供 `capture`、`install` 和 `restore`，使用固定 root helper 与 Docker 的单个 `ExecStartPre` drop-in。协调器先封存只读 `capture`，再安装。安装只接受缺失文件或内容、属主与权限完全匹配的已有文件；不覆盖冲突文件，也不重启 Docker。恢复保留原本已有的安装，并在重载失败时保留 helper，允许依据同一封存状态继续恢复。目录中出现其他内容时不递归删除。

该工具没有独立的配置切换入口。正式接入须明确绑定宿主网络组件，核对 systemd 实际加载了指定 `ExecStartPre`，再执行已批准的维护。回退先恢复代理和桥接参数，再恢复本批启动 hook，最后按原始存在状态恢复当前规则。

切换到纯 NAT 前先建立保护。Docker 重启后核对保护、数据库进程、所有批准绑定、回环访问和容器回连。验证应包含原始二层报文正负对照，不能只用普通 TCP 连接判断回环端口边界。

回退须先恢复原用户态代理和程序，再恢复桥接接口的 `route_localnet` 原值，随后按封存的原始状态移除本次保护。原本缺省的 `live-restore` 需要先显式重载为原值，再恢复配置文件原始存在状态；删除配置项本身不代表该运行值已经回退。配置目录属性、Docker 身份文件和其他容器状态也需要读回。

此处规则及顺序来自针对 Docker 20.10.24 的隔离验证设计。实际主机的打包版本、内核、服务管理器和网络差异须另行核实。Docker 对 live restore 的重载和适用条件见[官方说明](https://docs.docker.com/engine/daemon/live-restore/)，20.10.24 的重载行为见[该版本源码](https://github.com/moby/moby/blob/v20.10.24/daemon/reload.go)。

## 已执行的隔离验证

实际无外网 Linux 执行 18 项工具测试，并在 Docker 20.10.24、overlay2 与真实临时 MariaDB 容器中完成 120 个私有 TCP、64 个私有 UDP、3 个回环 TCP 的全路径验证。内核 NAT、同配置 daemon 重启和回退均通过，已建立 TCP 连接可在同配置 daemon 重启后继续通信。故意遗漏一个发布端口时检测到 186/187，通过恢复后重新达到 187/187。

原始二层 SYN 探针设有可达的私有地址正对照；受保护的 NAT、重启和回退均未扩大回环端口可达范围。数据库容器/原生 PID/启动时间/镜像/重启数和持久标记保持；原始配置文件存在状态、目录属性、Docker 身份文件摘要、三个停止历史容器以及桥接参数均恢复，临时业务 schema 最终为零。

持久化 helper 和 hook 的安装、重复安装、恢复与启动前 helper 执行已在该 Linux 环境验证；服务管理器调用使用受限替身，启动前动作由夹具按 `ExecStartPre` 等价顺序执行。该结果不代表实际 systemd 开机流程已验证，也不代表生产安装完成。隔离环境的 Alpine 内核和静态 Docker 包与实际主机 Debian 内核/打包版本仍有差异。
