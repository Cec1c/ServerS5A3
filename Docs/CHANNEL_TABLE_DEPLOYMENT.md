# 部署时选择正确的外部频道表

本仓120频道部署方案使用下列外部表。**首次准备资源时就显式传入 `--channel-info`；不要把PVF内置频道表提取结果作为默认部署目录。** 已有实例先读回实际加载的表，再核对差异。

| 文件 | 用途 | SHA256 |
| --- | --- | --- |
| [channel-reference-120.csv](assets/channel-reference-120.csv) | 原118条清单的PVP与帝国修订，2026-10-29 | `6adf30783f0a94f4989881ca0a0bca4363868c363012cd9e205f9f896f161be7` |
| [完整外部 channel_info.etc](../go-server/internal/services/dnfbridge/testdata/channel_info.etc) | 与清单逐项一致的运行输入，含区域、副本映射及频道名称引用 | `e04a80759359d6fd5e17c78831a554e3a7faa050896a9ad8975d98515ec87aeb` |

CSV中的频道号、区域标识、类型、端口均已与完整表核对；120个频道号和120个端口分别唯一，默认端口为 `10000 + 频道号`。“监听正常”列保留历史记录并标明本轮待验证项，部署后须重新检查。CSV不能直接传给 `--channel-info`，因为它不包含完整表中的区域副本映射、名称引用等字段。

## Agent的选择顺序

1. 记录客户端EXE、PVF和外部频道表的版本及SHA256。采用本方案时，使用上表链接的完整ETC并核对CSV；用户指定其他目录时，记录替代依据与逐项差异。
2. 已有实例先读取 `instance.json` 中的 `channel_file`、实际挂载文件及运行进程的 `DNFBRIDGE_CHANNEL_INFO_PATH`。保留已批准的外部表；更换时备份旧文件、源配置和生成结果。
3. PVF用于任务、物品、地图和频道名称等资源匹配。升级PVF时保留独立频道目录，不能因PVF内置表能解析，就覆盖已选外部表。
4. 缺少完整外部表时先取得上表文件或用户指定的已批准文件；不要从CSV编造缺少字段，也不要用PVF提取结果代替缺失输入。

历史验证中，PVF内置的38条server-1目录未能完成游戏登录，而118条外部目录完成了登录、建角和初始场景流程，见[Docker部署说明](DOCKER_LOCAL.md)。部署时核对实际条目，不能只看文件名或端口连通。

## PVP与帝国频道修订（2026-10-29）

本表由原118条扩展为120条，原有111条非PVP记录保持。PVP旧501..507调整为191..197，增加75无双频道和187帝国频道；无双75及排位191使用类型8，手动房间192..197使用类型24，PVP等级范围1..90。帝国使用类型39、`[tagtournament]`、等级90..90，并补320/336区域副本映射。

新增或改号的9条记录在CSV标为“待验证”；其它“是”沿用原导出记录，均不代表修订后已经监听或实机通过。频道号和默认端口一并变化，旧10501..10507改为10191..10197，新增10075与10187。超时空和亡者峡谷等其它条目本次保持，不能将这次PVP范围验收扩大到全部专用频道。

**已有部署不会因仓库文件更新而自动生效。** `prepare --channel-info`会将源表复制到运行目录的`channel.override.etc`；已有副本内容不同时会拒绝覆盖。管理员须先核对并保留自己的有效外部配置，按批准的配置替换流程同步实际加载表，再通过正常部署入口生成频道目录、监听/映射端口与资源摘要，重启服务，让客户端重新下载同一来源的目录。仅替换源码中的测试数据或仅拉取Git，不能当作已经修复线上频道。

本次不修改任务2670门槛、客户端EXE或PVF，也不代表目标服的“任务未完成”提示已实机复现或消除。验收包括：未完成任务在原频道拒绝；完成第一步后立即进入；已完成角色重登后进入；PVP/帝国返回普通频道；实际加载表与客户端下载表及端口一致。历史source-manifest文件保留原导入阶段记录，当前表摘要以上表为准。

## 显式准备资源

Linux，在仓库根目录使用已有Docker入口。替换PVF路径与目标机器实际私网地址：

```sh
sh deploy/docker/manage.sh build
sh deploy/docker/manage.sh prepare \
  --pvf "/srv/dnf-assets/Script.pvf" \
  --channel-info "go-server/internal/services/dnfbridge/testdata/channel_info.etc" \
  --host-ip 192.168.1.50
```

Windows对应入口：

```bat
DOCKER.bat prepare --pvf "D:\your-client\client\Script.pvf" --channel-info "go-server\internal\services\dnfbridge\testdata\channel_info.etc" --host-ip 192.168.1.50
```

`prepare` 将表保存为 `runtime/docker/data/channel.override.etc`，在源配置记录 `channel_file` 和 `channel_source`，再生成目录清单、监听端口、Compose和环境文件。若已有不同的外部表，入口会拒绝覆盖；核对并备份后按本次批准的替换流程处理。已有远端实例沿用其实际runtime目录、部署用户和更新入口。

单独离线校验时，在 `go-server/` 目录使用现有资源工具，输出选择新的runtime路径：

```sh
go run ./cmd/server/assets \
  -pvf "/srv/dnf-assets/Script.pvf" \
  -channel "internal/services/dnfbridge/testdata/channel_info.etc" \
  -output "../runtime/channel-check/channel.override.etc"
```

检查JSON输出的 `source` 以 `external:` 开头，`channels=120`、`sha256` 等于上表摘要，`ports` 与CSV逐项一致。该工具也会读取所选PVF；最终输出目录来自明确指定的外部表。

## 部署验收

| 检查层 | 应核对的事实 |
| --- | --- |
| 目录内容 | 120条记录的频道号、区域、类型、端口全部匹配，包括23号安徒恩、28号卢克及其后续频道 |
| 生成配置 | `channel_file=channel.override.etc`；`manifest.json`记录外部来源、完整摘要及120个端口 |
| 实际进程 | `DNFBRIDGE_CHANNEL_INFO_PATH` 指向已批准文件，容器挂载内容与生成配置一致 |
| 网络 | 所需游戏端口实际监听且玩家侧可达，目录通告地址属于本次目标服务器 |
| 客户端目录 | 在线频道脚本与动态目录使用同一外部表，首次安全落点及普通/团队目录过滤按匹配EXE核验 |
| 游戏操作 | 登录、选角进城、普通频道切换及团队页显示/切换实测，记录失败时间与连接信息 |

本表含91个type11普通频道、4个type23安徒恩频道、7个type32频道（卢克团队4、卢克普通3）；另有黄金之路、远古、时空裂缝、超时空、决斗、帝国竞技场和亡者峡谷。完整目录包含大于255的频道号，不能为凑目录数量把它们塞入仅支持u8的客户端入口。

表一致但团队页仍为空时，检查 `default_group_login_only`、客户端目录过滤和首次落点策略。选角卡住时关联会话、进城日志及数据库等待。端口可达、容器healthy与游戏操作分别按实际证据记录。
