# Docker 运行配置包与脱敏环境参考

[返回项目首页](../README.md) · [构建与部署指南](BUILD_AND_DEPLOY.md) · [推荐实例配置预设包](../deploy/templates/docker-instance.recommended.json) · [脱敏 server.env 参考](../deploy/templates/docker-server.redacted.env)

配套新实例的协议、玩法与数据库兼容缺省值来自 [`docker-instance.recommended.json`](../deploy/templates/docker-instance.recommended.json)。`prepare` 根据实例和客户端身份生成运行配置，`up` 会先执行同一准备步骤。

> [!IMPORTANT]
> **`runtime/docker/server.env` 与 `compose.json` 是每次 `prepare` / `up` 自动重生成的产物，请勿直接手改 `server.env`。** 所有持久开关统一写在 `runtime/docker/instance.json` 中，再运行 `sh deploy/docker/manage.sh prepare` 或 `up` 生成。直接裸跑 `DNF90Server` 二进制而未加载这些环境变量，会导致青龙/黄龙大会进图零怪直接通关、双组 Buff 不保存、公会与深渊异常等大量“假 BUG”。

## 运行目录结构

```text
runtime/docker/
├── instance.json          唯一人工/脚本维护的实例配置源（地址、凭据与全部玩法开关）
├── compose.json           manage.py 自动生成的容器、挂载、端口与内存限制配置
├── server.env             manage.py 自动生成的游戏服务环境变量
├── database.env           manage.py 自动生成的 MariaDB 数据库凭据
├── data/                  Script.pvf、channel.override.etc（120频道外部表）、manifest.json
└── server/
    ├── configs/           自动生成的 dnfbridge.toml、logic.toml、plan.json
    └── logs/              游戏运行日志
```

使用 `--runtime-dir` 时，上述路径相对所选目录。数据库卷、随机生成的密码与管理令牌仅保存在目标机器本地，切勿上传到公开仓库或 Issue。

## 推荐配置与公网通告

首次 `prepare` 为配套新实例自动应用推荐配置，包括三大会刷怪、公会、积分、双组 Buff、分解结果和城镇显示。赛马 2365 的新实例默认值为关闭。已识别的 v56 仍采用旧协议设置。旧库的 2365 管理保存值优先，按[活动开关说明](BUILD_AND_DEPLOY.md#已有服检查保存值实际生效值和目录)核对并关闭。

配套旧实例可以补齐缺失字段。普通双组已开启或 EXE 摘要已识别为支持双组时，顶层和 `runtime_compatibility` 中已显式填写的值都会保留。发生迁移时，准备程序在运行目录写入 `instance.before-recommended-*.json`。该备份包含实例凭据。需要回退时，用该文件恢复 `instance.json`，再重新准备和启动。

```sh
sh deploy/docker/manage.sh prepare --apply-recommended
sh deploy/docker/manage.sh up
```

更换客户端时重新传 `--client-dir`，由准备程序读取新的 `DNF.exe` 摘要。目录缺少 EXE 时，准备会在修改已保存的实例前停止。

公网或 NAT 搭建时，`--host-ip` 仍填服务器网卡的私网地址，另传 `--public-host <公网IPv4>`。准备程序把公网地址持久化到 `instance.json` 的 `public_host`。生成的频道目录和游戏/中继通告使用公网地址，Compose 的网卡绑定使用私网地址。要恢复局域网通告，可从实例配置删除 `public_host`，再重新准备和启动。

站点路由或云防火墙须将频道 TCP 和中继 UDP 的实际端口映射到私网宿主机。数据库、管理、账号 API 和本地 bootstrap 绑定方式保持与原入口一致，公网登录接入仍走已配置的登录器或网关。配置生成可验证地址与端口，外网实际连通性需要从玩家网络核对。

## 核心协议与玩法开关详解

### 1. 客户端协议与显示配对（`official-24.9` / V210–V215+）

| `instance.json` 字段 | 推荐值 | 生成的 `server.env` 键 | 作用与不配置的后果 |
| --- | --- | --- | --- |
| `client_version` | `"official-24.9"` | `DNFBRIDGE_CURRENT_CLIENT_VERSION=official-24.9` | 启用匹配现行客户端的协议号与 606 字节令牌登录帧 |
| `body_codec` / `client_body_codec` | `"plaintext"` | `DNFBRIDGE_GAME_UPPER_*_BODY_CODEC=plaintext` | 上下行明文包体编解码（配合生成器固定的 `HEADER=server16`、`DPROTO_MODE=legacy_patch`） |
| `buff_group_sync` | `"on"` | `DNFBRIDGE_BUFF_GROUP_SYNC=on` | V212–V215+ 双组 8 字节 1426 Buff 换装登记协议；未开时双组登记不保存、切图被清空（旧 v56 客户端须设为 `"off"`，详见 [Buff 部署说明](BUFF_GROUP_DEPLOYMENT.md)） |
| `buff_swap_reapply` | `"on"` | `DNFBRIDGE_BUFF_SWAP_REAPPLY=on` | 登记 Buff 换装栏物品变动后按选定技能即时重算生效 |
| `tag_buff_group_sync` | `"auto"` | `DNFBRIDGE_TAG_BUFF_GROUP_SYNC` | 已启用普通双组且 `client_dir` 中的 EXE 通过配对核验时开启帝国双组 |
| `runtime_compatibility.DNFBRIDGE_MILEAGE_SHOP_ENABLED` | `"on"` | `DNFBRIDGE_MILEAGE_SHOP_ENABLED=on` | 启用原生积分账本和商城。客户端须接收余额通知 427 |
| `disjoint_epic_display` | `"paired-v1"` | `DNFBRIDGE_DISJOINT_EPIC_DISPLAY=paired-v1` | 配套 V210+ 客户端正常弹出史诗分解无色/史诗灵魂结算结果框 |
| `town_display_only` | `"on"` | `DNFBRIDGE_TOWN_DISPLAY_ONLY=on` | 配套 V211+ 客户端启用共享城镇非队友轻量外观初始化，大幅降低主城同屏重加载开销 |
| `channel_directory_owner_range` | `"u8"` | `DNFBRIDGE_CHANNEL_DIRECTORY_OWNER_RANGE=u8` | 保证 120 频道目录（含 187 帝国、191–197 PVP 等 >127 频道号）按无符号字节正确表达 |

### 2. 青龙/黄龙/魂大会与赛马活动开关（最易踩坑项）

**普通黄龙（120）、普通青龙（121）及魂·黄龙/青龙大会必须设置 `dungeon_tournament: "vendor"`：**

```dotenv
DNFBRIDGE_DUNGEON_TOURNAMENT=vendor
```

- **为什么不配就会“进图不出怪、直接判通关”**：大会地图的 `.map` 文件没有常规 `[monster]` 怪物段，16 队 4 轮对手来自 `[tournament enemies]` 并通过赛程通知 `386/387` 下发。当开关处于默认 `off` 时，房间在构造期因怪物数为 0 直接被判定为 `Cleared`，表现为进场无怪直接胜利或卡在结算。
- **严格字面量校验**：[大会开关实现](../go-server/internal/services/dnfbridge/dungeon_tournament_config.go) 只把 `vendor` 识别为开启；未设置、空值、`off`、**甚至手写成 `on`、`true`、`1` 全部都会被当作关闭**！实例生成器只接受 `"off"` 或 `"vendor"`。资源与入口核对详见 [大会入口说明](seeker/QST-015-016-tournament-entry.md)。
- **赛马活动 2365 必须继续保持关闭**：设置 `"joust_event_enabled": false`（`DNFBRIDGE_JOUST_EVENT_ENABLED=false`）。若复用旧数据库，数据库中已保存的活动状态优先于环境变量，还需按 [活动开关说明](BUILD_AND_DEPLOY.md#当前活动开关与赛马关闭方法) 核对 `effectiveEnabled=false` 与 `pendingRestart=false`。

### 3. 公会子系统开关

`manage.py` 默认将公会扩展功能全部置为关闭或空策略。合入推荐预设包后会开启：

- `"guild_ranking_enabled": true`、`"guild_essence_enabled": true`、`"guild_hongbao_enabled": true`：启用公会排行榜、公会精髓与公会红包页面及交易；
- `"guild_contribution_policy": "daily-v1"`：启用公会每日贡献策略；
- `"guild_guardian_policy"`：配置公会守护神首次免费解锁与初始技能等级（须包含 `first_purchase`、`initial_level`、`initial_skills`、`first_select` 四个完整字段）。
- `"guild_solo_create"`：默认 `false`（按原规则建会），单人测试服可按需改为 `true` 允许单人直接创建公会。

### 4. `runtime_compatibility` 兼容与性能开关

推荐预设包中的 `runtime_compatibility` 汇集了正式服长期运行验证的协议与存储档位：

| 环境变量键 | 推荐值 | 说明 |
| --- | --- | --- |
| `DNFBRIDGE_EXPERIMENTAL_PEAK` | `"on"` | 启用拍卖写事务锁外回包、事务内有界角色快照复用与升级属性批量写入 |
| `DNFBRIDGE_MYSQL_INSERT_BATCH` | `"200"` | 多行批量插入大小（1–1000），消除删插路径逐行往返 |
| `DNFBRIDGE_INVENTORY_SAVE_SCOPE` | `"slot"` | 背包按变更槽位增量落库，避免整包全量重写 |
| `DNFBRIDGE_EQUIPMENT_SAVE_SCOPE` | `"entry"` | 穿戴装备按变更部位词条增量落库 |
| `DNFBRIDGE_HELL_BLOCKED_SLOTS` | `"on"` | 放行深渊派对入场选项与受控槽位协议 |
| `DNFBRIDGE_DUNGEON_ENTRY_SELECTION_MODE` | `"allow"` | 允许匹配的地下城难度与入场模式选择 |
| `DNFBRIDGE_DUNGEON_DEATH_ROOM_PROOF` | `"room_proven"` | 怪物击杀结合服务端房间自证，防止跨房误判 |
| `DNFBRIDGE_PARTY_FORMATION_ORDER` | `"vendor_wire"` | 按厂商报文序列下发组队编队与端点顺序 |
| `DNFBRIDGE_PARTY_PEER_ENDPOINTS_AFTER_ROSTER` / `REBROADCAST` | `"repeat"` / `"on"` | 组队名单变更后补发与重播队友 UDP 端点，降低组队断连与隐身 |
| `DNFBRIDGE_PARTY_SELECTOR_MEMBER_SELF_PREPARE` / `ALL_MEMBER_USER_STATE` | `"on"` / `"all"` | 组队选图界面队员自准备与全员状态同步 |
| `DNFBRIDGE_OWNER_CONTEXT_USE_CLIENT_CHANNEL` | `"on"` | 场景对象 owner 上下文对齐客户端当前落点频道 |
| `DNFBRIDGE_PEER_USERINFO_IDENTITY_CARRIER_MODE0_GUARD` | `"on"` | 同屏角色身份载体 `mode0` 预演保护 |
| `DNFBRIDGE_TOWN_NPC_EVENT_CATALOG` | `"on"` | 下发城镇活动 NPC 目录附加项 |
| `DNFBRIDGE_LOGIN_AREA_GATES` | `"strict"` | 登录落点按角色已解锁区域门槛严格校验 |

### 5. 多人服容量模板（可选）

对于 **4 核 16 GiB** 同机运行游戏与 MariaDB 的服务器，可在 `instance.json` 顶层增加 `"performance_profile": "shared-4c-16g"`（自动设置 `server_memory_limit="10g"`、`go_memory_limit="8GiB"`、`database_memory_limit="3g"`、`database_buffer_pool_size="1g"`），也可根据机器实际内存显式指定 `database_memory_limit` 与 `database_buffer_pool_size`（缓冲池不得超过数据库容器内存上限的 60% 且须预留至少 256 MiB 余量）。

## 只读核对运行中游戏进程的真实生效值

容器启动后，不要只看磁盘上的配置文件，建议在 Linux 宿主机运行以下只读脚本读取 `DNF90Server` 进程的 `/proc/<pid>/environ`，确认关键开关已真正进入运行进程（自定义项目名或路径请按实际替换）：

```sh
container_id=$(docker compose -p servers5a3-local -f runtime/docker/compose.json ps -q server)
game_pid=$(docker top "$container_id" -eo pid,comm | awk '$2 == "DNF90Server" {print $1}')
sudo python3 - "$game_pid" <<'PY'
import pathlib
import sys

pid = sys.argv[1]
if not pid.isdecimal():
    raise SystemExit("Expected one running DNF90Server process")
keys = (
    "DNFBRIDGE_CURRENT_CLIENT_VERSION",
    "DNFBRIDGE_GAME_UPPER_HEADER",
    "DNFBRIDGE_GAME_UPPER_BODY_CODEC",
    "DNFBRIDGE_GAME_UPPER_CLIENT_BODY_CODEC",
    "DNFBRIDGE_DPROTO_MODE",
    "DNFBRIDGE_DUNGEON_TOURNAMENT",
    "DNFBRIDGE_JOUST_EVENT_ENABLED",
    "DNFBRIDGE_BUFF_GROUP_SYNC",
    "DNFBRIDGE_BUFF_SWAP_REAPPLY",
    "DNFBRIDGE_TAG_BUFF_GROUP_SYNC",
    "DNFBRIDGE_MILEAGE_SHOP_ENABLED",
    "DNFBRIDGE_DISJOINT_EPIC_DISPLAY",
    "DNFBRIDGE_TOWN_DISPLAY_ONLY",
    "DNFBRIDGE_EXPERIMENTAL_PEAK",
    "DNFBRIDGE_INVENTORY_SAVE_SCOPE",
    "DNFBRIDGE_EQUIPMENT_SAVE_SCOPE",
)
raw = pathlib.Path(f"/proc/{pid}/environ").read_bytes().decode()
values = dict(row.split("=", 1) for row in raw.split("\0") if "=" in row)
for key in keys:
    print(f"{key}={values.get(key, '<UNSET>')}")
PY
```

文件中的预期值与进程输出应完全一致（如 `DNFBRIDGE_DUNGEON_TOURNAMENT=vendor`、`DNFBRIDGE_JOUST_EVENT_ENABLED=false`）。活动 2365 的数据库持久化覆盖另按 [活动管理说明](BUILD_AND_DEPLOY.md#已有服检查保存值实际生效值和目录) 核对。

## 向他人分享配置时的脱敏规则

对外交流排障可直接引用公开的 [`docker-instance.recommended.json`](../deploy/templates/docker-instance.recommended.json) 和 [`docker-server.redacted.env`](../deploy/templates/docker-server.redacted.env)。若需贴出自己实例的配置片段，请务必将以下私密字段替换为占位符，切勿整包发送 `runtime/docker/`、数据库备份或原始抓包日志：

- 数据库连接串（`DNFBRIDGE_ACCOUNT_API_DSN`、`database_password`、`root_password`）；
- 管理令牌、代理密钥、登录签名密钥（`admin_token`、`player_admin_secret`、`player_proxy_secret` 等；注：`DNFBRIDGE_ACCOUNT_API_LOGIN_TOKEN=true` 仅为布尔开关，不是密钥）；
- 服务器真实公网/内网 IP、域名、SSH 凭据与个人目录路径。
