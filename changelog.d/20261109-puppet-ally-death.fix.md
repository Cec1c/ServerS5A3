---
category: 功能修复
---

# 动态人偶击杀的死亡上报兼容

## 变更

- 新增有界"召唤源租约"：角色用掉一件 PVF 写了 `[summon apc]` 的消耗品后，登记一个到期时间取该段寿命（缺省 15 分钟）的租约。租约挂在会话上，跨副本运行有效，到期或换角色即失效。
- 租约存在时，接受 `owner=0xffff` + 恰好一条 14 字节贡献（kind `0x111`、贡献值非零、键非零）的 op39；目标仍必须是本房间已宣告、仍存活、阻挡型的活怪（普通 [monster] 行或运行期扩展 actor 都覆盖），且非 Boss、非清房条件来源。
- 贡献键按房间收集：同一房间内人偶本体与各台召唤物各有自己的键（现场 0:1 房间是 2，1:1 房间连续出现 18 / 35 / 77），因此按房间维护键集合、上限 32；换房间时客户端会重建 actor 并重新分配键，进入新房间后清空重收。
- 目标完全不在服务端房间表里时（地图 act 脚本 `[SUMMON MONSTER]` 在客户端本地召出来的怪），在有租约的前提下**只回发一条死亡通知**让客户端收尸：不提交死亡、不给奖励、不触发清房，每房上限 64 条。
- 合法上报沿用原有死亡提交、op38、经验/掉落与清房链路；不强制清房，不改变奖励与过图判定。
- 新增两条不受 `DNFBRIDGE_PACKET_LOG` 影响的 INFO 诊断：`dnfbridge puppet ally contribution accepted` / `... rejected`（带房间、目标键、贡献键与具体拒绝原因）。形状不吻合的上报不记录。
- 剧情 APC 不放行：本图声明了非敌对 AI 角色但贡献者未公告时，继续按原严格规则拒绝。

## 原因

现场（2026-11-09 测试服，幽暗密林 3 号副本）玩家只用人偶打怪：客户端发出 `owner=ffff`、单条 `0x111` 贡献（28 号键 2、遗迹少女键 8）的 op39，贡献键由客户端从贡献者对象虚表 `+0x54` 取回，服务端没有登记来源（`CmdPacketSummonMonster` 无 handler，抓包中客户端也从不发）。原逻辑以 `non_hostile_ai_request_shape_unproven` 拒收，怪在服务端仍是活怪、房间不清，客户端却已本地判死，表现为白怪且无法过门。

## 验证

- 新增 Linux 专项：无凭证拒绝、租约提交并写 op38、同房第二个键拒绝、换房重绑、租约过期拒绝、贡献键冒用已宣告 actor 拒绝、未知目标拒绝、友方 AI 角色声明识别。
- `TestEkernFactionDeath*`、`TestOfficialFriendlyAPC*`、`TestHandleDungeonMonsterDeathRoomProofSwitchMatrix`、`TestCurrentDungeonOwnerSentinel*` 相关回归全部通过。
- 全量包在 Windows 上有既有的环境性失败（`character_option_entry_regression46`、`high_channel_reconnect`、`raid_channel_reconnect`、`dungeon_settlement_phase_order` 等）；已在 HEAD 干净工作树复核同批失败，与本次改动无关。服务端仍按 Linux 门禁验证。
- 测试服实机：首版（每租约一键）第 1 房间通过、第 2/3 房间仍白，据此改为每房一键并加诊断；本版待复测。

## 范围与风险

- Boss 与清房条件目标同样放行：人偶把 Boss 打死之后如果拒收，客户端会留一只白 Boss、本局作废，这比"人偶能打 Boss"严重得多。结算走原有 Boss 链路（`bossDieCheck`、清房条件照旧），边界仍由租约、本房在册活怪、阻挡怪三条守住。
- 只覆盖"本局消耗过召唤道具"这一种来源；不影响玩家、队友、已登记剧情 APC、艾肯阵营与教程等既有放行路径。
- 残余风险：使用了人偶的改包客户端可以在本房间（含 Boss 房）击杀在册阻挡怪。跨房目标、本房已宣告 actor 的键、未在册怪物都已被排除；本服已有秒杀工具，边际风险按此评估。
