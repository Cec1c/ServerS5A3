# 2026-11-23 公开功能累计搭建入口

这份说明按“从公开仓库和公开资源搭建后能否使用功能”核对交付。使用最新 `master`、本页累计资源和仓库推荐配置；旧版 Release 列表中的 Latest 标记不能代替这组配套。

## 下载及核验

下载[累计公开配套包](https://github.com/Cec1c/ServerS5A3/releases/tag/client-v217-public-native-cumulative-20261123)的 `client-v217-public-native-cumulative-20261123.zip`。它包含 `DNF.exe`、`Script.pvf`、`DpsMeter.dll`、`CombatPower.dll`、`AtlasWorkers101.dll` 和 `ImagePacks2/sprite_interface_private_features20261113.NPK`，以及 `manifest.json`。这是用于已有 official-24.9 完整客户端的更新包。完整基础客户端的其他 NPK、运行库和正常登录入口仍须保留。

Windows：先解压到独立目录，运行仓库中的 `resource-recipes\selected-native-20261123\VERIFY_CLIENT.bat <解压目录>`，全部六个组件通过后，备份原六项并安装到原客户端相同相对路径。Linux 准备目录可用 `python3 resource-recipes/selected-native-20261123/verify_bundle.py <解压目录>` 核验（Python 3.11+）。客户端关闭时替换；EXE 新增静态导入的 AtlasWorkers DLL 必须同时安装。

| 产物 | SHA-256 |
| --- | --- |
| DNF.exe | `19233532acda5b6cea91e70662157a34dac769f6a5755222fe04fbd8cd41019f` |
| Script.pvf | `40c123c53e31009d7e0ff2568413221ccb4fdbb417f37387cbc730890fa51cb4` |
| AtlasWorkers101.dll | `83b5bdec6cd3d3e81f951e945a1d8de19abf99d0757d1d3819aceb86d25ea6d9` |

其余组件及字节数见[完整清单](../resource-recipes/selected-native-20261123/manifest.json)。服务端、客户端与独立 GM 查询使用同一份 PVF；资源索引也必须由这份 PVF 生成。

## 首次搭建

```sh
git clone https://github.com/Cec1c/ServerS5A3.git
cd ServerS5A3
git pull --ff-only
sh deploy/docker/manage.sh build
sh deploy/docker/manage.sh prepare \
  --pvf /srv/dnf-assets/Script.pvf \
  --client-dir /srv/dnf-assets \
  --channel-info go-server/internal/services/dnfbridge/testdata/channel_info.etc \
  --host-ip 192.168.1.50
sh deploy/docker/manage.sh up
sh deploy/docker/manage.sh status
```

`/srv/dnf-assets` 是已核验更新包所在目录，地址换成服务器实际网卡地址。公网/NAT 部署另设 `--public-host <公网IPv4>`，核对 TCP、UDP 转发和登录器通告地址。新配套实例自动应用 `docker-instance.recommended.json`，包括 vendor 大会、双组 Buff、积分、换装重算、公会及协议修复；赛马 2365 关闭。不需要再复制预设覆盖整个实例文件。

准备工具根据真实 EXE 摘要选择消息格式，新增累计 EXE 已登记。只上传 PVF、指向错误的客户端目录、或者强开旧客户端不支持的协议，均不能视为配套完成。原来的登录器与独立 GM 仍按 [BUILD_AND_DEPLOY](BUILD_AND_DEPLOY.md) 和 README 的对应仓库接入。

## 已有实例升级

保存数据库、原程序、六项资源和实例配置的回退版本。更新源码并重新构建，以原来的运行目录、项目名、镜像和配套资源路径执行 `prepare --apply-recommended`，再按维护流程更新游戏容器。已有 PVF 不同时，依[资源替换步骤](BUILD_AND_DEPLOY.md)先备份再更换。

`--apply-recommended` 只补缺项，保留已有显式值。检查 `buff_group_sync`、`buff_swap_reapply`、`tag_buff_group_sync`、`runtime_compatibility` 中积分与协议项；不要把“代码已更新”当成“旧的关闭值已改写”。`server.env` 是生成文件，应修改 `instance.json`。核对[实际进程环境](DOCKER_RUNTIME_REFERENCE.md)，确保运行的是新构建。

史诗之路使用活动 350 和累计奖励活动 216。official-24.9 新实例默认启用 216；旧数据库中保存的关闭值优先，需经活动管理读取 `revision` 后用 `save_activities` 更新对应项，重启后核对 `effectiveEnabled=true`、`pendingRestart=false`。赛马 2365 保持关闭。累计奖励按角色每日通关寂静之森一次计一天，06:00 换日，8/16/24 个有效活动日达到领取门槛；进度不是同一天反复通关累计。

## 本批可获得的功能

| 项目 | 公开搭建后的行为 | 必要配套 |
| --- | --- | --- |
| 贪食／无尽贪食升级 | 九种图纸走原生合成、核对材料并保留被升级装备身份 | 新服务端、配套 PVF |
| 异次元装备升级 | 按实际 10158122 道具规则走原生升级请求及事务 | 新服务端、配套 PVF |
| 觉醒装扮选项 | 开关保存、登录同步，保留其他选项位 | 新服务端 |
| 超时空怪物对应 | 战况表选定遭遇与进入地图、兵营和本部 BOSS 一致 | 新服务端、累计 EXE |
| 史诗之路累计奖励 | 原生面板收到有效天数及 8/16/24 日门槛状态 | 新服务端、活动 350/216 |
| 寻龙 100341606 | 穿戴时地面金币增加 30%，重放不重复加钱 | 新服务端 |
| 公会光环券 | 原生 action 126 使用、事务扣券与槽位解锁 | 新服务端、配套 PVF |
| 拾取显示 | 普通拾取显示新增量；已持久化重放按当前槽位数量刷新；结算不重复叠加 | 新服务端与累计 EXE |
| 超时空个人武器盒 | 直接翻牌加代币分支补抽，合计每人 0.8% | 新 PVF、已有公开结算逻辑 |
| 公会扩容 | 上架 5000 人容量购买项 19 和契约项 27 | 新 PVF、已有公开公会逻辑 |
| FPS 与图形处理 | 滑块 60–300 及末端“不限”档（保存值 320）；原生保存、预算和等待路径配套；SSE 写入和最多六线程图集预解压 | 累计 EXE、AtlasWorkers DLL |

保留此前公开的积分余额通知 427、超时空兵营修复进度、罗什第四帧防护、黑钻与加百利、双组 Buff、旧版 DPS 和战力面板等累计能力。午间组队更新已在[公开 PR #123](https://github.com/Cec1c/ServerS5A3/pull/123)合入：加载确认、跨过图重发、返城保存及队伍处理按该批代码生效；本批修复遗留测试与当前中继实现不匹配的问题。

NPC 已退场但后续攻击仍触发的友方 APC 路径修复也在公开源码中。它覆盖已定位的 NPC 击杀后残留攻击问题；其他白怪现象仍应按各自复现条件判断。

## 概率与验证边界

超时空个人池权重依次为 3744、450、36、135、135、500，总计 5000。武器盒直接概率 `36/5000`，10% 代币分支补抽原生池概率 `500/5000 × 36/4500`，合计 `0.008`。PVF 只修改该奖励表和公会表，其他 1,126,603 个条目均经工具验证保持。

检查包括真实公开 PVF 专项、相关 Linux 测试/vet/构建、部署配对检查及客户端指令模拟。图形模拟验证滑块、保存与等待分支，以及两个各 1024 组 SSE 等价用例；AtlasWorkers 校验 896 项正常解压、1792 项错误输入和资源缺失回落。自动检查与玩家在游戏内操作的验收分别记录；本批累计包的多人团本画面和玩家体验仍需实机测试，性能提升比例不作保证。

公开玩法采用本页列出的源码、配置和资源。自定义服务器的额外政策、私有管理工具、玩家数据及未授权功能不属于公开交付；与其差异不应被当成公开功能遗漏。

源码重建见[累计资源配方](../resource-recipes/selected-native-20261123/README.md)。
