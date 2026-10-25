# 公会基地商人购买与升级

公会大厅购买命令 1354 和升级命令 840 缺少处理入口，购买后客户端停留在等待状态。正式 V174 还拒收升级 ACK 840 与等级通知 778；升级发送器只初始化 25 字节正文的末 12 字节。

## 实现

- 1354 使用四个 u32：物品、数量、商店、当前等级 NPC。目录沿真实 PVF 的商店列表、基础 NPC 等级映射、实际 NPC 角色、商店商品和物品费用交叉解析。两类商人各十级，购买权限沿用服务端公会共享购买 bit8。
- 840 忽略旧客户端未初始化的前 13 字节，读取基础 NPC、当前等级、目标等级。基础 NPC 390/389 分别对应防御/佣兵；仅允许逐级升级，费用按 `building/mecenary` 的目标级减一行读取，第一列 AP、第二列 GP。原生窗口与确认链分别检查 bit16 和 bit8，服务端同时验证。
- 领域事务重验角色、账号、有效会籍、权限、场景与当前等级。购买的 AP、真实背包和固定次日 06:00 期限共同提交；升级的 AP/GP、持久等级共同提交。永久回执绑定请求身份及正文，重试不重扣、不重发、不延长期限。
- 旧存档无商人字段时两级均为 1；非空状态严格验证 1..10。应答后读取当前持久状态发送 778/1297 与库存刷新，同会成员通过已有事件队列接收最新状态。旧升级回执不会把客户端等级降回旧值。
- 客户端补丁接受完整 V174 摘要，使用既有接收节空闲空间，只新增 class1/840 与 class0/778 门控，并清零发送前缀；保留红包、基地数字刷新等累计逻辑，输出独立候选和精确差异清单。

## 验证入口

```text
go test -buildvcs=false -count=1 ./internal/modules/dnf/guild -run Merchant
go test -buildvcs=false -count=1 ./internal/services/dnfbridge -run GuildMerchant
go test -race -buildvcs=false -count=1 ./internal/modules/dnf/guild ./internal/services/dnfbridge -run Merchant
python scripts/client/test_guild_merchants_v174.py -v
```

真实提取资源测试用 `GUILD_MERCHANT_EXTRACTED_ROOT`，其 `pvf`、`pvf-meta`、`pvf-maps` 子目录保存商店/物品/NPC、索引与 agitwar、实际地图。`GUILD_MERCHANT_UPGRADE_EXTRACTED_ETC` 指向升级表；`DNFBRIDGE_PLAYER_TEST_DSN` 仅使用一次性数据库。客户端用 `GUILD_MERCHANT_V174_EXE`，生产报文通过 `GUILD_ESSENCE_NATIVE_STAGE_DIR` 导出并由 `GUILD_MERCHANT_PACKET_DIR` 原生消费。

覆盖两类商人、连续交易、旧请求重放、身份/会籍/权限/场景/距离拒绝、余额不足、满栏、数量与目录不符、软删除角色、并发最后余额及 SQL 晚失败回滚。真实提取目录有 20 店、132 种商品，137 个商品货架组合逐一购买；原生验证发送前缀、ACK 解锁、等级字段、完整接收矩阵和 Windows 只读映像。

## 发布与验收

先发行兼容客户端并核验玩家实际通道，再部署依赖服务端。保留当前 PVF 与外部频道表。新增商人状态与回执的兼容构建是回退基线，避免旧写入者丢弃未知账本字段。

游戏内验收：首次与连续购买到账且精气准确；不足或满栏后仍可再次操作；升级后等级/货架变化，同会成员与重登显示一致。源码验证、发行、部署和玩家实测分别留回执。
