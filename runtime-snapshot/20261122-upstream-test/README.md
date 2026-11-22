# 运行时快照（2026-11-22，测试容器）

本目录是验证本 PR 时使用的服务端运行时配置，**已脱敏**。直接使用前请先替换占位符：

| 占位符 | 替换为 |
|---|---|
| `<HOST_IP>` | 你的公网/内网 IP（客户端连接用的地址） |
| `CHANGE_ME` | 你自己的数据库密码 / 各种 secret |
| `10.253.90.2` / `10.253.90.10` | 你的 Compose 内网地址（可按需改成别的网段） |

## 文件

| 文件 | 用途 |
|---|---|
| `compose-test.json` | 单服务端容器编排：发布 7001/17002/10010-10508/2311-2312udp 等端口，健康检查走 17002 `/api/status` |
| `server.env` | 服务端环境变量，**第四节开关都在这里** |
| `database.env` | 数据库账号（`MARIADB_*`） |
| `server/configs/dnfbridge.toml` | 服务端主配置（监听地址、admin http） |
| `server/configs/dnf/logic.toml` | 逻辑层配置（频道、脚本版本等） |
| `server/configs/servergroup/plan.json` | 分组计划 |
| `server/client-attest.json` | 客户端校验文件（`mode: off` = 关闭校验） |

## 启动

```bash
# 1) 替换占位符
sed -i 's/<HOST_IP>/你的IP/g' compose-test.json server.env
sed -i 's/CHANGE_ME/你的密码/g' server.env database.env

# 2) 准备服务端运行目录（二进制 + 配置 + data）
mkdir -p server && cp server.env database.env server/ 2>/dev/null || true

# 3) 启动
docker compose -p dnf-test -f compose-test.json up -d
```

## 关键开关（与仓库 Docs/PARTY_PORT_FROM_DNF90.md 第四节一致）

```env
DNFBRIDGE_SAME_PARTY_TOWN_SELECTION_NOTICE=on   # 城镇同队队员右键菜单
DNFBRIDGE_DUNGEON_PEER_RECOVERY=off             # ARQ1 重投链（关闭=修复卡顿）
DNFBRIDGE_DUNGEON_PEER_OP37_RESEND=off          # 换房不重投 op37
DNFBRIDGE_DUNGEON_PEER_ROOM_RESEND=off          # 换房不重投同房间 actor
DNFBRIDGE_PARTY_CARD_FLIP_ROW_MODEL=vendor      # op71 行状态：队伍槽位
```
